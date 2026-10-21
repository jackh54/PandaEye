package sqlstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) InsertAudit(ctx context.Context, e *store.AuditEntry) error {
	if e.ID == "" {
		e.ID = identity.NewUUID().String()
	}
	e.CreatedAt = orNow(e.CreatedAt)
	detail := string(e.Detail)
	if len(e.Detail) == 0 {
		detail = "{}"
	}
	_, err := s.exec(ctx, s.w, `INSERT INTO audit_log (id, tenant_id, principal, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.ID, e.TenantID, e.Principal, e.Action, e.Target, detail, e.CreatedAt.UnixMicro())
	return err
}

func (s *Store) ListAudit(ctx context.Context, tenantID string, page store.Page) ([]store.AuditEntry, string, error) {
	cond, cargs, err := pageCond(page, "created_at", "id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT id, principal, action, target, detail, created_at FROM audit_log WHERE tenant_id = ?`+cond+
		` ORDER BY created_at DESC, id DESC LIMIT ?`, append(append([]any{tenantID}, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.AuditEntry
	for rows.Next() {
		e := store.AuditEntry{TenantID: tenantID}
		var detail string
		var created int64
		if err := rows.Scan(&e.ID, &e.Principal, &e.Action, &e.Target, &detail, &created); err != nil {
			return nil, "", err
		}
		e.Detail, e.CreatedAt = json.RawMessage(detail), time.UnixMicro(created).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.ID }), nil
}

func (s *Store) PurgeAudit(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM audit_log WHERE tenant_id = ? AND created_at < ?`, tenantID, before.UnixMicro())
}
