// Dashboard-side audit log (D1). Mutations through the API are additionally audited by the API itself.

export function auditStmt(
  db: D1Database,
  entry: { tenantId: string | null; principal: string; action: string; target?: string | null; detail?: unknown; now: number },
): D1PreparedStatement {
  return db
    .prepare("INSERT INTO audit_log (tenant_id, principal, action, target, detail, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6)")
    .bind(
      entry.tenantId,
      entry.principal.slice(0, 200),
      entry.action.slice(0, 100),
      entry.target ? entry.target.slice(0, 200) : null,
      entry.detail === undefined ? null : JSON.stringify(entry.detail).slice(0, 2000),
      entry.now,
    );
}

export async function audit(db: D1Database, entry: Parameters<typeof auditStmt>[1]): Promise<void> {
  await auditStmt(db, entry).run();
}

export interface DashboardAuditRow {
  id: number;
  principal: string;
  action: string;
  target: string | null;
  detail: string | null;
  created_at: number;
}

export async function recentAudit(db: D1Database, tenantId: string, limit = 100): Promise<DashboardAuditRow[]> {
  const r = await db
    .prepare("SELECT id, principal, action, target, detail, created_at FROM audit_log WHERE tenant_id = ?1 ORDER BY id DESC LIMIT ?2")
    .bind(tenantId, limit)
    .all<DashboardAuditRow>();
  return r.results ?? [];
}
