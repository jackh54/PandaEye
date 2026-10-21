// Sessions (ARCHITECTURE section 7): cookie `__Host-pe_session` carries a random 256-bit id; D1 stores only
// sha256(id). 12 h absolute lifetime, 2 h idle timeout, a per-session CSRF token, rotated on login and
// deleted on disable / role change / password change.

import { randomToken, sha256Hex } from "./crypto";
import { isRole, type Role } from "./roles";

export const SESSION_COOKIE = "__Host-pe_session";
export const SESSION_ABSOLUTE_MS = 12 * 60 * 60_000;
export const SESSION_IDLE_MS = 2 * 60 * 60_000;
/** last_seen_at is written at most this often (fewer D1 writes; idle precision stays ~1 min). */
export const SESSION_TOUCH_MS = 60_000;

const RAW_ID_RE = /^[A-Za-z0-9_-]{43}$/;

export interface SessionUser {
  id: string;
  tenantId: string;
  username: string;
  role: Role;
}

export interface Session {
  idHash: string;
  csrf: string;
  createdAt: number;
  lastSeenAt: number;
  expiresAt: number;
  user: SessionUser;
}

export interface NewSession {
  id: string;
  csrf: string;
  expiresAt: number;
}

export async function createSession(db: D1Database, userId: string, now: number): Promise<NewSession> {
  const id = randomToken(32);
  const idHash = await sha256Hex(id);
  const csrf = randomToken(32);
  const expiresAt = now + SESSION_ABSOLUTE_MS;
  await db.batch([
    db
      .prepare("DELETE FROM sessions WHERE user_id = ?1 AND (expires_at <= ?2 OR last_seen_at <= ?3)")
      .bind(userId, now, now - SESSION_IDLE_MS),
    db
      .prepare(
        "INSERT INTO sessions (id_hash, user_id, csrf, created_at, last_seen_at, expires_at) VALUES (?1, ?2, ?3, ?4, ?4, ?5)",
      )
      .bind(idHash, userId, csrf, now, expiresAt),
  ]);
  return { id, csrf, expiresAt };
}

interface SessionRow {
  id_hash: string;
  csrf: string;
  created_at: number;
  last_seen_at: number;
  expires_at: number;
  user_id: string;
  tenant_id: string;
  username: string;
  role: string;
  disabled: number;
}

/** Loads a live session (and refreshes last_seen_at). Expired, idle or disabled-user sessions are deleted. */
export async function loadSession(db: D1Database, rawId: string | undefined, now: number): Promise<Session | null> {
  if (!rawId || !RAW_ID_RE.test(rawId)) return null;
  const idHash = await sha256Hex(rawId);
  const row = await db
    .prepare(
      `SELECT s.id_hash, s.csrf, s.created_at, s.last_seen_at, s.expires_at,
              u.id AS user_id, u.tenant_id, u.username, u.role, u.disabled
         FROM sessions s JOIN users u ON u.id = s.user_id
        WHERE s.id_hash = ?1`,
    )
    .bind(idHash)
    .first<SessionRow>();
  if (!row) return null;
  const expired = row.expires_at <= now || row.last_seen_at <= now - SESSION_IDLE_MS;
  if (expired || row.disabled || !isRole(row.role)) {
    await db.prepare("DELETE FROM sessions WHERE id_hash = ?1").bind(idHash).run();
    return null;
  }
  let lastSeenAt = row.last_seen_at;
  if (now - row.last_seen_at >= SESSION_TOUCH_MS) {
    await db.prepare("UPDATE sessions SET last_seen_at = ?2 WHERE id_hash = ?1").bind(idHash, now).run();
    lastSeenAt = now;
  }
  return {
    idHash: row.id_hash,
    csrf: row.csrf,
    createdAt: row.created_at,
    lastSeenAt,
    expiresAt: row.expires_at,
    user: { id: row.user_id, tenantId: row.tenant_id, username: row.username, role: row.role },
  };
}

export async function destroySession(db: D1Database, rawId: string | undefined): Promise<void> {
  if (!rawId || !RAW_ID_RE.test(rawId)) return;
  await db.prepare("DELETE FROM sessions WHERE id_hash = ?1").bind(await sha256Hex(rawId)).run();
}

export function destroyUserSessionsStmt(db: D1Database, userId: string): D1PreparedStatement {
  return db.prepare("DELETE FROM sessions WHERE user_id = ?1").bind(userId);
}

export async function destroyUserSessions(db: D1Database, userId: string): Promise<void> {
  await destroyUserSessionsStmt(db, userId).run();
}
