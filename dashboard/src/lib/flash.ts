// Read-once, session-bound flash rows for post-redirect-get (see migrations/0002_flashes.sql).

import { b64decode, b64url, randomBytes, randomToken, sha256, utf8 } from "./crypto";

export const FLASH_TTL_MS = 10 * 60_000;
const FLASH_ID_RE = /^[A-Za-z0-9_-]{22}$/;

export type FlashKind = "apikey" | "invite" | "erasure";

/** The browser's session: raw cookie value (key material) and its stored hash (binding). */
export interface FlashSession {
  raw: string;
  hash: string;
}

async function flashKey(rawSessionId: string): Promise<CryptoKey> {
  const material = await sha256(`pandaeye-flash-v1:${rawSessionId}`);
  return crypto.subtle.importKey("raw", material, "AES-GCM", false, ["encrypt", "decrypt"]);
}

export async function putFlash(db: D1Database, s: FlashSession, kind: FlashKind, payload: unknown, now: number): Promise<string> {
  const id = randomToken(16);
  const iv = randomBytes(12);
  const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv, additionalData: utf8(`${id}|${kind}|${s.hash}`) }, await flashKey(s.raw), utf8(JSON.stringify(payload)));
  await db.batch([
    db.prepare("DELETE FROM flashes WHERE session_hash = ?1 AND expires_at <= ?2").bind(s.hash, now),
    db
      .prepare("INSERT INTO flashes (id, session_hash, kind, data, expires_at) VALUES (?1, ?2, ?3, ?4, ?5)")
      .bind(id, s.hash, kind, `${b64url(iv)}.${b64url(new Uint8Array(ct))}`, now + FLASH_TTL_MS),
  ]);
  return id;
}

/** Returns the payload once (the row is deleted atomically), or null when missing, expired or foreign. */
export async function takeFlash<T>(db: D1Database, s: FlashSession, kind: FlashKind, id: string | undefined, now: number): Promise<T | null> {
  if (!id || !FLASH_ID_RE.test(id)) return null;
  const row = await db
    .prepare("DELETE FROM flashes WHERE id = ?1 AND session_hash = ?2 AND kind = ?3 RETURNING data, expires_at")
    .bind(id, s.hash, kind)
    .first<{ data: string; expires_at: number }>();
  if (!row || row.expires_at <= now) return null;
  try {
    const [ivPart, ctPart] = row.data.split(".");
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: b64decode(ivPart ?? ""), additionalData: utf8(`${id}|${kind}|${s.hash}`) },
      await flashKey(s.raw),
      b64decode(ctPart ?? ""),
    );
    return JSON.parse(new TextDecoder().decode(plain)) as T;
  } catch {
    return null;
  }
}
