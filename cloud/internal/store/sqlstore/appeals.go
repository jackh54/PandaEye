package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

const appealCols = `id, tenant_id, verdict_id, player_uuid, message, contact, status, created_at, decided_at, decided_by, decision_note`

func scanAppeal(sc interface{ Scan(...any) error }) (*store.Appeal, error) {
	var a store.Appeal
	var created, decided sql.NullInt64
	if err := sc.Scan(&a.ID, &a.TenantID, &a.VerdictID, &a.PlayerUUID, &a.Message, &a.Contact, &a.Status, &created, &decided,
		&a.DecidedBy, &a.DecisionNote); err != nil {
		return nil, mapErr(err)
	}
	a.CreatedAt, a.DecidedAt = fromUS(created), fromUS(decided)
	return &a, nil
}

func (s *Store) CreateAppeal(ctx context.Context, a *store.Appeal, lim store.AppealLimits) error {
	if a.ID == "" {
		a.ID = identity.NewUUID().String()
	}
	a.CreatedAt = orNow(a.CreatedAt)
	a.Status = store.AppealOpen
	return s.tx(ctx, func(tx *sql.Tx) error {
		// Lock the verdict row so concurrent appeals for it are counted one after the other.
		var id string
		if err := s.queryRow(ctx, tx, `SELECT id FROM verdicts WHERE id = ? AND tenant_id = ?`+s.forUpdate(), a.VerdictID, a.TenantID).Scan(&id); err != nil {
			return mapErr(err)
		}
		var open, total, recent int
		if err := s.queryRow(ctx, tx, `SELECT
				COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0), COUNT(*)
			FROM appeals WHERE tenant_id = ? AND verdict_id = ?`, store.AppealOpen, a.TenantID, a.VerdictID).Scan(&open, &total); err != nil {
			return mapErr(err)
		}
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM appeals WHERE tenant_id = ? AND created_at > ?`,
			a.TenantID, a.CreatedAt.Add(-24*time.Hour).UnixMicro()).Scan(&recent); err != nil {
			return mapErr(err)
		}
		if open >= lim.OpenPerVerdict || total >= lim.TotalPerVerdict || recent >= lim.PerTenantPerDay {
			return store.ErrLimitExceeded
		}
		_, err := s.exec(ctx, tx, `INSERT INTO appeals (`+appealCols+`) VALUES (`+placeholders(11)+`)`,
			a.ID, a.TenantID, a.VerdictID, a.PlayerUUID, a.Message, a.Contact, a.Status, a.CreatedAt.UnixMicro(),
			us(a.DecidedAt), a.DecidedBy, a.DecisionNote)
		return err
	})
}

func (s *Store) GetAppeal(ctx context.Context, tenantID, id string) (*store.Appeal, error) {
	return scanAppeal(s.queryRow(ctx, s.r, `SELECT `+appealCols+` FROM appeals WHERE id = ? AND tenant_id = ?`, id, tenantID))
}

func (s *Store) ListAppeals(ctx context.Context, tenantID string, f store.AppealFilter, page store.Page) ([]store.Appeal, string, error) {
	where := " WHERE tenant_id = ?"
	args := []any{tenantID}
	if f.Status != "" {
		where += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.VerdictID != "" {
		where += " AND verdict_id = ?"
		args = append(args, f.VerdictID)
	}
	cond, cargs, err := pageCond(page, "created_at", "id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT `+appealCols+` FROM appeals`+where+cond+` ORDER BY created_at DESC, id DESC LIMIT ?`,
		append(append(args, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Appeal
	for rows.Next() {
		a, err := scanAppeal(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.ID }), nil
}

func (s *Store) DecideAppeal(ctx context.Context, tenantID, id, status, by, note string, at time.Time) (*store.Appeal, error) {
	var a *store.Appeal
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `UPDATE appeals SET status = ?, decided_at = ?, decided_by = ?, decision_note = ?
			WHERE id = ? AND tenant_id = ? AND status = ?`, status, orNow(at).UnixMicro(), by, note, id, tenantID, store.AppealOpen)
		if err != nil {
			return err
		}
		a, err = scanAppeal(s.queryRow(ctx, tx, `SELECT `+appealCols+` FROM appeals WHERE id = ? AND tenant_id = ?`, id, tenantID))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrConflict
		}
		return nil
	})
	return a, err
}

func (s *Store) PurgeAppeals(ctx context.Context, tenantID string, before, contactBefore time.Time) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `UPDATE appeals SET contact = '' WHERE tenant_id = ? AND decided_at IS NOT NULL AND decided_at < ? AND contact <> ''`,
			tenantID, contactBefore.UnixMicro()); err != nil {
			return err
		}
		res, err := s.exec(ctx, tx, `DELETE FROM appeals WHERE tenant_id = ? AND created_at < ?`, tenantID, before.UnixMicro())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
