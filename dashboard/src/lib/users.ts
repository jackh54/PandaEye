// D1 user and invite storage. Every query is scoped by tenant_id where a tenant is known.

import { auditStmt } from "./audit";
import { randomToken, sha256Hex } from "./crypto";
import { can, invitableRoles, isRole, ROLE_RANK, ROLES, type Role } from "./roles";
import { destroyUserSessionsStmt } from "./session";

export const INVITE_TTL_MS = 48 * 60 * 60_000;

// An invite is only as good as its creator's current authority: the creator (alias `c`) must be enabled, in
// the invite's tenant, and still allowed to issue the invite's role (alias `i`). Built from constants only.
function rankSql(col: string): string {
  return `(CASE ${col} ${ROLES.map((r) => `WHEN '${r}' THEN ${ROLE_RANK[r]}`).join(" ")} ELSE 0 END)`;
}
const INVITER_ROLES = ROLES.filter((r) => can(r, "invites:manage"))
  .map((r) => `'${r}'`)
  .join(", ");
const CREATOR_STILL_ALLOWED = `c.disabled = 0 AND c.tenant_id = i.tenant_id AND c.role IN (${INVITER_ROLES}) AND ${rankSql("c.role")} > ${rankSql("i.role")}`;
const INVITE_ROLES: Role[] = ["admin", "staff", "viewer"];

/** Deletes unused invites of `creatorId` whose role the creator may no longer issue as `role`. */
function revokeInvitesBeyondStmt(db: D1Database, tenantId: string, creatorId: string, role: Role | null): D1PreparedStatement | null {
  const allowed = role ? invitableRoles(role) : [];
  const revoked = INVITE_ROLES.filter((r) => !allowed.includes(r));
  if (!revoked.length) return null;
  return db
    .prepare(
      `DELETE FROM invites WHERE created_by = ?1 AND tenant_id = ?2 AND used_at IS NULL AND role IN (${revoked.map((_, k) => `?${k + 3}`).join(", ")})`,
    )
    .bind(creatorId, tenantId, ...revoked);
}

export interface UserRow {
  id: string;
  tenant_id: string;
  username: string;
  password_hash: string;
  role: Role;
  created_at: number;
  disabled: number;
  password_changed_at: number;
}

export function newUserId(): string {
  return `u_${randomToken(15)}`;
}

export async function findUserByUsername(db: D1Database, username: string): Promise<UserRow | null> {
  const row = await db.prepare("SELECT * FROM users WHERE username = ?1").bind(username).first<UserRow>();
  return row && isRole(row.role) ? row : null;
}

export async function findUser(db: D1Database, tenantId: string, id: string): Promise<UserRow | null> {
  const row = await db.prepare("SELECT * FROM users WHERE id = ?1 AND tenant_id = ?2").bind(id, tenantId).first<UserRow>();
  return row && isRole(row.role) ? row : null;
}

export async function listUsers(db: D1Database, tenantId: string): Promise<UserRow[]> {
  const r = await db
    .prepare("SELECT * FROM users WHERE tenant_id = ?1 ORDER BY created_at ASC LIMIT 500")
    .bind(tenantId)
    .all<UserRow>();
  return (r.results ?? []).filter((u) => isRole(u.role));
}

export async function countUsers(db: D1Database, tenantId: string): Promise<number> {
  const row = await db.prepare("SELECT COUNT(*) AS n FROM users WHERE tenant_id = ?1").bind(tenantId).first<{ n: number }>();
  return row?.n ?? 0;
}

export function isUniqueViolation(e: unknown): boolean {
  return e instanceof Error && /UNIQUE constraint failed/i.test(e.message);
}

export type CreateResult = { ok: true } | { ok: false; reason: "tenant_has_users" | "username_taken" | "invite_invalid" };

/**
 * Creates the tenant's owner only while the tenant has zero users. The check and the insert are one
 * conditional INSERT ... SELECT ... WHERE NOT EXISTS executed in a D1 batch (a transaction), so two
 * concurrent setups cannot both succeed.
 */
export async function createOwnerIfNone(
  db: D1Database,
  u: { id: string; tenantId: string; username: string; passwordHash: string; now: number; slug: string },
): Promise<CreateResult> {
  try {
    const [insert] = await db.batch([
      db
        .prepare(
          `INSERT INTO users (id, tenant_id, username, password_hash, role, created_at, disabled, password_changed_at)
           SELECT ?1, ?2, ?3, ?4, 'owner', ?5, 0, ?5
            WHERE NOT EXISTS (SELECT 1 FROM users WHERE tenant_id = ?2)`,
        )
        .bind(u.id, u.tenantId, u.username, u.passwordHash, u.now),
      db
        .prepare(
          `INSERT INTO audit_log (tenant_id, principal, action, target, detail, created_at)
           SELECT ?1, ?2, 'setup.owner_created', ?3, ?4, ?5 WHERE EXISTS (SELECT 1 FROM users WHERE id = ?3)`,
        )
        .bind(u.tenantId, `user:${u.username}`, u.id, JSON.stringify({ slug: u.slug }), u.now),
    ]);
    return (insert?.meta.changes ?? 0) === 1 ? { ok: true } : { ok: false, reason: "tenant_has_users" };
  } catch (e) {
    if (isUniqueViolation(e)) return { ok: false, reason: "username_taken" };
    throw e;
  }
}

export interface InviteRow {
  token_hash: string;
  tenant_id: string;
  role: Role;
  expires_at: number;
  used_at: number | null;
  used_by: string | null;
  created_by: string;
  created_at: number;
}

export async function createInvite(
  db: D1Database,
  i: { tenantId: string; role: Role; createdBy: string; principal: string; now: number },
): Promise<{ token: string; expiresAt: number }> {
  const token = randomToken(32);
  const hash = await sha256Hex(token);
  const expiresAt = i.now + INVITE_TTL_MS;
  await db.batch([
    db
      .prepare(
        "INSERT INTO invites (token_hash, tenant_id, role, expires_at, used_at, used_by, created_by, created_at) VALUES (?1, ?2, ?3, ?4, NULL, NULL, ?5, ?6)",
      )
      .bind(hash, i.tenantId, i.role, expiresAt, i.createdBy, i.now),
    auditStmt(db, { tenantId: i.tenantId, principal: i.principal, action: "invite.created", target: hash.slice(0, 16), detail: { role: i.role }, now: i.now }),
  ]);
  return { token, expiresAt };
}

export async function findValidInvite(db: D1Database, token: string, now: number): Promise<InviteRow | null> {
  if (!/^[A-Za-z0-9_-]{43}$/.test(token)) return null;
  const row = await db
    .prepare(
      `SELECT i.* FROM invites i JOIN users c ON c.id = i.created_by
        WHERE i.token_hash = ?1 AND i.used_at IS NULL AND i.expires_at > ?2 AND ${CREATOR_STILL_ALLOWED}`,
    )
    .bind(await sha256Hex(token), now)
    .first<InviteRow>();
  return row && isRole(row.role) ? row : null;
}

export async function listPendingInvites(db: D1Database, tenantId: string, now: number): Promise<InviteRow[]> {
  const r = await db
    .prepare("SELECT * FROM invites WHERE tenant_id = ?1 AND used_at IS NULL AND expires_at > ?2 ORDER BY created_at DESC LIMIT 200")
    .bind(tenantId, now)
    .all<InviteRow>();
  return r.results ?? [];
}

/**
 * Consumes an invite and creates the user atomically (batch = transaction). The invite must be unused,
 * unexpired, and its creator must still be enabled and allowed to issue its role.
 */
export async function acceptInvite(
  db: D1Database,
  a: { token: string; userId: string; username: string; passwordHash: string; now: number },
): Promise<CreateResult & { tenantId?: string }> {
  if (!/^[A-Za-z0-9_-]{43}$/.test(a.token)) return { ok: false, reason: "invite_invalid" };
  const hash = await sha256Hex(a.token);
  try {
    const [insert] = await db.batch([
      db
        .prepare(
          `INSERT INTO users (id, tenant_id, username, password_hash, role, created_at, disabled, password_changed_at)
           SELECT ?1, i.tenant_id, ?2, ?3, i.role, ?4, 0, ?4 FROM invites i JOIN users c ON c.id = i.created_by
            WHERE i.token_hash = ?5 AND i.used_at IS NULL AND i.expires_at > ?4 AND ${CREATOR_STILL_ALLOWED}`,
        )
        .bind(a.userId, a.username, a.passwordHash, a.now, hash),
      db
        .prepare(
          "UPDATE invites SET used_at = ?2, used_by = ?3 WHERE token_hash = ?1 AND used_at IS NULL AND EXISTS (SELECT 1 FROM users WHERE id = ?3)",
        )
        .bind(hash, a.now, a.userId),
      db
        .prepare(
          `INSERT INTO audit_log (tenant_id, principal, action, target, detail, created_at)
           SELECT tenant_id, ?2, 'invite.accepted', ?3, NULL, ?4 FROM users WHERE id = ?1`,
        )
        .bind(a.userId, `user:${a.username}`, hash.slice(0, 16), a.now),
    ]);
    if ((insert?.meta.changes ?? 0) !== 1) return { ok: false, reason: "invite_invalid" };
    const row = await db.prepare("SELECT tenant_id FROM users WHERE id = ?1").bind(a.userId).first<{ tenant_id: string }>();
    return { ok: true, tenantId: row?.tenant_id };
  } catch (e) {
    if (isUniqueViolation(e)) return { ok: false, reason: "username_taken" };
    throw e;
  }
}

export async function revokeInvite(db: D1Database, tenantId: string, hash: string, principal: string, now: number): Promise<boolean> {
  const [r] = await db.batch([
    db.prepare("DELETE FROM invites WHERE token_hash = ?1 AND tenant_id = ?2 AND used_at IS NULL").bind(hash, tenantId),
    auditStmt(db, { tenantId, principal, action: "invite.revoked", target: hash.slice(0, 16), now }),
  ]);
  return (r?.meta.changes ?? 0) === 1;
}

/**
 * Role change: sessions of the user are deleted, and so are the user's unused invites for roles the new role
 * may not issue, in the same transaction.
 */
export async function setUserRole(db: D1Database, tenantId: string, userId: string, role: Role, principal: string, now: number): Promise<boolean> {
  const revoke = revokeInvitesBeyondStmt(db, tenantId, userId, role);
  const [r] = await db.batch([
    db.prepare("UPDATE users SET role = ?3 WHERE id = ?1 AND tenant_id = ?2").bind(userId, tenantId, role),
    destroyUserSessionsStmt(db, userId),
    ...(revoke ? [revoke] : []),
    auditStmt(db, { tenantId, principal, action: "user.role_changed", target: userId, detail: { role }, now }),
  ]);
  return (r?.meta.changes ?? 0) === 1;
}

/** Disabling deletes the user's sessions and unused invites in the same transaction. */
export async function setUserDisabled(db: D1Database, tenantId: string, userId: string, disabled: boolean, principal: string, now: number): Promise<boolean> {
  const revoke = disabled ? revokeInvitesBeyondStmt(db, tenantId, userId, null) : null;
  const [r] = await db.batch([
    db.prepare("UPDATE users SET disabled = ?3 WHERE id = ?1 AND tenant_id = ?2").bind(userId, tenantId, disabled ? 1 : 0),
    destroyUserSessionsStmt(db, userId),
    ...(revoke ? [revoke] : []),
    auditStmt(db, { tenantId, principal, action: disabled ? "user.disabled" : "user.enabled", target: userId, now }),
  ]);
  return (r?.meta.changes ?? 0) === 1;
}

/** Password change: all sessions of the user are deleted in the same transaction. */
export async function setUserPassword(db: D1Database, user: { id: string; tenantId: string; username: string }, passwordHash: string, now: number): Promise<void> {
  await db.batch([
    db.prepare("UPDATE users SET password_hash = ?2, password_changed_at = ?3 WHERE id = ?1").bind(user.id, passwordHash, now),
    destroyUserSessionsStmt(db, user.id),
    auditStmt(db, { tenantId: user.tenantId, principal: `user:${user.username}`, action: "user.password_changed", target: user.id, now }),
  ]);
}

/** Hourly cleanup (cron). */
export async function cleanup(db: D1Database, now: number): Promise<void> {
  await db.batch([
    db.prepare("DELETE FROM sessions WHERE expires_at <= ?1 OR last_seen_at <= ?2").bind(now, now - 2 * 60 * 60_000),
    db.prepare("DELETE FROM invites WHERE (used_at IS NULL AND expires_at <= ?1) OR (used_at IS NOT NULL AND used_at <= ?2)").bind(now - 7 * 86_400_000, now - 30 * 86_400_000),
    db.prepare("DELETE FROM login_attempts WHERE window_start <= ?1 AND locked_until <= ?2").bind(now - 86_400_000, now),
    db.prepare("DELETE FROM audit_log WHERE created_at <= ?1").bind(now - 365 * 86_400_000),
    db.prepare("DELETE FROM flashes WHERE expires_at <= ?1").bind(now),
  ]);
}
