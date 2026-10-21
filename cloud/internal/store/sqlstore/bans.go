package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

const banCols = `verdict_id, tenant_id, server_id, player_uuid, active, reason, created_at, overturned_at, overturned_by,
	overturn_note, unban_verdict_id, unban_state`

func scanBan(sc interface{ Scan(...any) error }) (*store.Ban, error) {
	var b store.Ban
	var created, overturned sql.NullInt64
	if err := sc.Scan(&b.VerdictID, &b.TenantID, &b.ServerID, &b.PlayerUUID, &b.Active, &b.Reason, &created, &overturned,
		&b.OverturnedBy, &b.OverturnNote, &b.UnbanVerdictID, &b.UnbanState); err != nil {
		return nil, mapErr(err)
	}
	b.CreatedAt, b.OverturnedAt = fromUS(created), fromUS(overturned)
	return &b, nil
}

func (s *Store) GetBan(ctx context.Context, tenantID, verdictID string) (*store.Ban, error) {
	return scanBan(s.queryRow(ctx, s.r, `SELECT `+banCols+` FROM bans WHERE verdict_id = ? AND tenant_id = ?`, verdictID, tenantID))
}

func (s *Store) ListBans(ctx context.Context, tenantID string, f store.BanFilter, page store.Page) ([]store.Ban, string, error) {
	where := " WHERE tenant_id = ?"
	args := []any{tenantID}
	if f.Active != nil {
		where += " AND active = ?"
		args = append(args, *f.Active)
	}
	if f.PlayerUUID != "" {
		where += " AND player_uuid = ?"
		args = append(args, f.PlayerUUID)
	}
	cond, cargs, err := pageCond(page, "created_at", "verdict_id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT `+banCols+` FROM bans`+where+cond+` ORDER BY created_at DESC, verdict_id DESC LIMIT ?`,
		append(append(args, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Ban
	for rows.Next() {
		b, err := scanBan(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *b)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.VerdictID }), nil
}

func (s *Store) OverturnBan(ctx context.Context, tenantID, verdictID string, o store.BanOverturn) error {
	state := store.UnbanNone
	if o.UnbanVerdictID != "" {
		state = store.UnbanPending
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var active bool
		if err := s.queryRow(ctx, tx, `SELECT active FROM bans WHERE verdict_id = ? AND tenant_id = ?`+s.forUpdate(), verdictID, tenantID).Scan(&active); err != nil {
			return mapErr(err)
		}
		if !active {
			return store.ErrConflict
		}
		_, err := s.exec(ctx, tx, `UPDATE bans SET active = ?, overturned_at = ?, overturned_by = ?, overturn_note = ?,
			unban_verdict_id = ?, unban_state = ? WHERE verdict_id = ? AND tenant_id = ?`,
			false, orNow(o.At).UnixMicro(), o.By, o.Note, o.UnbanVerdictID, state, verdictID, tenantID)
		return err
	})
}
