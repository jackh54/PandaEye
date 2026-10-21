// Input validation shared by routes.

export const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** Opaque ids from the API (verdicts, clips, appeals, API keys, servers). */
export const ID_RE = /^[A-Za-z0-9_.:-]{1,128}$/;
export const USERNAME_RE = /^[a-z0-9][a-z0-9_.-]{2,31}$/;
export const APPEAL_CODE_RE = /^[A-Za-z0-9-]{4,32}$/;
export const LICENSE_KEY_RE = /^[\x21-\x7e]{8,256}$/;
export const CHECK_ID_RE = /^[a-z0-9_]+(\.[a-z0-9_]+)+$/;
export const OVERRIDE_KEY_RE = /^[a-z0-9_.]{1,64}$/;

export function isUuid(s: unknown): s is string {
  return typeof s === "string" && UUID_RE.test(s);
}

export function isId(s: unknown): s is string {
  return typeof s === "string" && ID_RE.test(s);
}

export function normalizeUsername(s: unknown): string {
  return typeof s === "string" ? s.trim().toLowerCase() : "";
}

export function isUsername(s: string): boolean {
  return USERNAME_RE.test(s);
}

/** Form field as a string (files and missing fields become ""). */
export function field(body: Record<string, unknown>, name: string): string {
  const v = body[name];
  return typeof v === "string" ? v : "";
}

export function checkbox(body: Record<string, unknown>, name: string): boolean {
  const v = body[name];
  return v === "on" || v === "1" || v === "true";
}

const DISCORD_HOSTS = new Set(["discord.com", "discordapp.com", "ptb.discord.com", "canary.discord.com"]);

/**
 * Discord webhook URL as accepted by the API (ARCHITECTURE 5.12): https, exact host, no userinfo/port,
 * /api/webhooks/<17-20 digits>/<60-80 [A-Za-z0-9_-]>, only `wait`/`thread_id` query parameters.
 */
export function isDiscordWebhook(raw: string): boolean {
  if (raw.length > 300) return false;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return false;
  }
  if (u.protocol !== "https:" || !DISCORD_HOSTS.has(u.hostname) || u.port !== "") return false;
  if (u.username !== "" || u.password !== "" || u.hash !== "") return false;
  // Exact textual host (no case tricks, no userinfo, no backslashes) as typed.
  if (!raw.startsWith(`https://${u.hostname}/api/webhooks/`)) return false;
  if (!/^\/api\/webhooks\/\d{17,20}\/[A-Za-z0-9_-]{60,80}$/.test(u.pathname)) return false;
  for (const key of u.searchParams.keys()) {
    if (key !== "wait" && key !== "thread_id") return false;
  }
  return true;
}

/** Same-origin relative path for post-login redirects; anything else becomes "/". */
export function safeNext(next: unknown): string {
  if (typeof next !== "string" || next.length > 512) return "/";
  if (!next.startsWith("/") || next.startsWith("//") || next.startsWith("/\\")) return "/";
  if (/[\x00-\x1f\\]/.test(next)) return "/";
  return next;
}

/** Parses `key=value, key=value` threshold overrides; numbers only. */
export function parseOverrides(raw: string): { ok: true; value: Record<string, number> } | { ok: false; error: string } {
  const value: Record<string, number> = {};
  const trimmed = raw.trim();
  if (!trimmed) return { ok: true, value };
  const parts = trimmed.split(/[,\n]/).map((p) => p.trim()).filter(Boolean);
  if (parts.length > 16) return { ok: false, error: "At most 16 overrides per check." };
  for (const part of parts) {
    const m = /^([^=\s]+)\s*=\s*(\S+)$/.exec(part);
    if (!m) return { ok: false, error: `Expected key=value, got "${part.slice(0, 40)}".` };
    const key = m[1]!;
    const num = Number(m[2]);
    if (!OVERRIDE_KEY_RE.test(key)) return { ok: false, error: `Invalid override name "${key.slice(0, 40)}".` };
    if (!Number.isFinite(num)) return { ok: false, error: `Override ${key} must be a number.` };
    value[key] = num;
  }
  return { ok: true, value };
}

export function formatOverrides(o: Record<string, number>): string {
  return Object.entries(o)
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}
