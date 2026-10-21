package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) BeginErasure(ctx context.Context, tenantID, player string, at time.Time) error {
	_, err := s.exec(ctx, s.w, `INSERT INTO erasures (tenant_id, player_uuid, requested_at, completed_at, counts) VALUES (?, ?, ?, NULL, ?)
		ON CONFLICT (tenant_id, player_uuid) DO UPDATE SET requested_at = excluded.requested_at, completed_at = NULL, counts = excluded.counts`,
		tenantID, player, orNow(at).UnixMicro(), "{}")
	return err
}

func (s *Store) EraseSubject(ctx context.Context, tenantID, player string) (map[string]int64, error) {
	counts := map[string]int64{}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		del := func(table, q string, args ...any) error {
			res, err := s.exec(ctx, tx, q, args...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			counts[table] += n
			return err
		}
		n, err := s.deleteClips(ctx, tx, tenantID, `(player_uuid = ? OR id IN (SELECT clip_id FROM clip_subjects WHERE tenant_id = ? AND player_uuid = ?))`,
			player, tenantID, player)
		if err != nil {
			return err
		}
		counts["clips"] = n
		steps := []struct{ table, q string }{
			{"player_names", `DELETE FROM player_names WHERE tenant_id = ? AND uuid = ?`},
			{"players", `DELETE FROM players WHERE tenant_id = ? AND uuid = ?`},
			{"appeals", `DELETE FROM appeals WHERE tenant_id = ? AND player_uuid = ?`},
			{"labels", `DELETE FROM labels WHERE tenant_id = ? AND player_uuid = ?`},
			{"reputation_contrib", `DELETE FROM reputation_contrib WHERE tenant_id = ? AND player_uuid = ?`},
		}
		for _, st := range steps {
			if err := del(st.table, st.q, tenantID, player); err != nil {
				return err
			}
		}
		if err := del("bans", `DELETE FROM bans WHERE tenant_id = ? AND player_uuid = ? AND active = ?`, tenantID, player, false); err != nil {
			return err
		}
		activeBan := `EXISTS (SELECT 1 FROM bans b WHERE b.verdict_id = verdicts.id AND b.tenant_id = verdicts.tenant_id AND b.active = ?)`
		if err := del("verdicts", `DELETE FROM verdicts WHERE tenant_id = ? AND player_uuid = ? AND NOT `+activeBan, tenantID, player, true); err != nil {
			return err
		}
		if err := del("verdicts_minimised", `UPDATE verdicts SET player_name = '', reason = '', evidence = ?, clip_ids = ?, payload = NULL
			WHERE tenant_id = ? AND player_uuid = ? AND `+activeBan, "{}", "[]", tenantID, player, true); err != nil {
			return err
		}
		_, err = s.exec(ctx, tx, `UPDATE bans SET reason = '' WHERE tenant_id = ? AND player_uuid = ?`, tenantID, player)
		return err
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

func (s *Store) CompleteErasure(ctx context.Context, tenantID, player string, counts map[string]int64, at time.Time) error {
	return s.updateOne(ctx, `UPDATE erasures SET completed_at = ?, counts = ? WHERE tenant_id = ? AND player_uuid = ?`,
		orNow(at).UnixMicro(), jsonText(counts, "{}"), tenantID, player)
}

func scanErasure(tenantID string, sc interface{ Scan(...any) error }) (*store.Erasure, error) {
	e := store.Erasure{TenantID: tenantID}
	var requested, completed sql.NullInt64
	var counts string
	if err := sc.Scan(&e.PlayerUUID, &requested, &completed, &counts); err != nil {
		return nil, mapErr(err)
	}
	e.RequestedAt, e.CompletedAt = fromUS(requested), fromUS(completed)
	if err := json.Unmarshal([]byte(counts), &e.Counts); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) GetErasure(ctx context.Context, tenantID, player string) (*store.Erasure, error) {
	return scanErasure(tenantID, s.queryRow(ctx, s.r, `SELECT player_uuid, requested_at, completed_at, counts FROM erasures
		WHERE tenant_id = ? AND player_uuid = ?`, tenantID, player))
}

func (s *Store) ListErasures(ctx context.Context, tenantID string, since time.Time) ([]store.Erasure, error) {
	rows, err := s.query(ctx, s.r, `SELECT player_uuid, requested_at, completed_at, counts FROM erasures
		WHERE tenant_id = ? AND requested_at >= ? ORDER BY requested_at, player_uuid`, tenantID, since.UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Erasure
	for rows.Next() {
		e, err := scanErasure(tenantID, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *Store) PurgeErasures(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM erasures WHERE tenant_id = ? AND requested_at < ?`, tenantID, before.UnixMicro())
}
