// Rate limiting backed by the D1 `login_attempts` table.
//
// * Fixed windows (per IP: login 10 / 15 min, setup 5 / h, ...): one atomic UPSERT ... RETURNING per hit.
// * Per-username exponential backoff: `count` = consecutive failures, `window_start` = last failure,
//   `locked_until` = earliest next attempt. Every attempt first claims the key with a conditional UPSERT
//   (so a burst of parallel requests at the moment a lock expires gets exactly one attempt), a failure
//   sets the next lock, a success deletes the row.
//
// Keys never contain raw IP addresses (see `ipKey`).

import { hmacSha256, hex } from "./crypto";

export interface WindowResult {
  allowed: boolean;
  count: number;
  retryAfterS: number;
}

export const LIMITS = {
  loginIp: { limit: 10, windowMs: 15 * 60_000 },
  setupIp: { limit: 5, windowMs: 60 * 60_000 },
  inviteIp: { limit: 10, windowMs: 15 * 60_000 },
  appealIp: { limit: 10, windowMs: 60 * 60_000 },
  passwordChangeUser: { limit: 10, windowMs: 15 * 60_000 },
} as const;

export async function hitFixedWindow(
  db: D1Database,
  key: string,
  limit: number,
  windowMs: number,
  now: number,
): Promise<WindowResult> {
  const row = await db
    .prepare(
      `INSERT INTO login_attempts (key, count, window_start, locked_until) VALUES (?1, 1, ?2, 0)
       ON CONFLICT (key) DO UPDATE SET
         count = CASE WHEN login_attempts.window_start <= ?2 - ?3 THEN 1 ELSE login_attempts.count + 1 END,
         window_start = CASE WHEN login_attempts.window_start <= ?2 - ?3 THEN ?2 ELSE login_attempts.window_start END
       RETURNING count, window_start`,
    )
    .bind(key, now, windowMs)
    .first<{ count: number; window_start: number }>();
  const count = row?.count ?? 1;
  const windowStart = row?.window_start ?? now;
  const allowed = count <= limit;
  return {
    allowed,
    count,
    retryAfterS: allowed ? 0 : Math.max(1, Math.ceil((windowStart + windowMs - now) / 1000)),
  };
}

/** Consecutive failures that are free before backoff starts. */
export const BACKOFF_FREE_FAILURES = 3;
export const BACKOFF_BASE_MS = 5_000;
export const BACKOFF_MAX_MS = 30 * 60_000;
/** Failure counters are forgotten after a day without failures. */
export const BACKOFF_RESET_MS = 24 * 60 * 60_000;
/** How long one attempt holds the claim (released by the failure/success update). */
const CLAIM_MS = 1_000;

/** Delay imposed after `failures` consecutive failures: 0, 0, 0, 5 s, 10 s, 20 s, ... capped at 30 min. */
export function backoffDelayMs(failures: number): number {
  if (failures < BACKOFF_FREE_FAILURES) return 0;
  const exp = failures - BACKOFF_FREE_FAILURES;
  if (exp >= 20) return BACKOFF_MAX_MS;
  return Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** exp);
}

export interface ClaimResult {
  allowed: boolean;
  retryAfterS: number;
  failures: number;
}

export async function claimBackoff(db: D1Database, key: string, now: number): Promise<ClaimResult> {
  const row = await db
    .prepare(
      `INSERT INTO login_attempts (key, count, window_start, locked_until) VALUES (?1, 0, ?2, ?2 + ?3)
       ON CONFLICT (key) DO UPDATE SET
         count = CASE WHEN login_attempts.window_start <= ?2 - ?4 THEN 0 ELSE login_attempts.count END,
         window_start = CASE WHEN login_attempts.window_start <= ?2 - ?4 THEN ?2 ELSE login_attempts.window_start END,
         locked_until = ?2 + ?3
       WHERE login_attempts.locked_until <= ?2
       RETURNING count`,
    )
    .bind(key, now, CLAIM_MS, BACKOFF_RESET_MS)
    .first<{ count: number }>();
  if (row) return { allowed: true, retryAfterS: 0, failures: row.count };
  const locked = await db
    .prepare("SELECT count, locked_until FROM login_attempts WHERE key = ?1")
    .bind(key)
    .first<{ count: number; locked_until: number }>();
  const until = locked?.locked_until ?? now + CLAIM_MS;
  return { allowed: false, retryAfterS: Math.max(1, Math.ceil((until - now) / 1000)), failures: locked?.count ?? 0 };
}

export async function recordBackoffFailure(db: D1Database, key: string, now: number): Promise<number> {
  const row = await db
    .prepare(
      `UPDATE login_attempts SET count = count + 1, window_start = ?2 WHERE key = ?1 RETURNING count`,
    )
    .bind(key, now)
    .first<{ count: number }>();
  const failures = row?.count ?? 1;
  await db
    .prepare("UPDATE login_attempts SET locked_until = ?2 WHERE key = ?1")
    .bind(key, now + backoffDelayMs(failures))
    .run();
  return failures;
}

export async function clearBackoff(db: D1Database, key: string): Promise<void> {
  await db.prepare("DELETE FROM login_attempts WHERE key = ?1").bind(key).run();
}

/** Expands an IPv6 address and keeps its /64 prefix; IPv4 addresses are kept whole. */
export function ipPrefix(ip: string): string {
  const s = ip.trim().toLowerCase();
  if (!s.includes(":")) return s;
  const [head = "", tail = ""] = s.split("::");
  const h = head ? head.split(":") : [];
  const t = s.includes("::") && tail ? tail.split(":") : [];
  const groups = s.includes("::") ? [...h, ...Array(Math.max(0, 8 - h.length - t.length)).fill("0"), ...t] : h;
  return groups
    .slice(0, 4)
    .map((g) => g.replace(/^0+(?=.)/, ""))
    .join(":") + "::/64";
}

/** Pseudonymous rate-limit key for an IP: HMAC(pepper, prefix), truncated. */
export async function ipKey(scope: string, ip: string, pepper: string): Promise<string> {
  const mac = await hmacSha256(pepper || "pandaeye", `ip:${ipPrefix(ip)}`);
  return `${scope}:ip:${hex(mac.subarray(0, 16))}`;
}

export function userKey(scope: string, username: string): string {
  return `${scope}:user:${username.toLowerCase().slice(0, 64)}`;
}
