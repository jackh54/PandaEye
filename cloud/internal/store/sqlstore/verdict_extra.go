package sqlstore

import (
	"context"
	"database/sql"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func (s *Store) AddReputationContribIfConfirmed(ctx context.Context, c store.ReputationContrib) (bool, error) {
	ok := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, `SELECT COUNT(*) FROM verdicts WHERE id = ? AND tenant_id = ? AND player_uuid = ?
			AND action = ? AND shadow = ? AND executed = ? AND status = ?`,
			c.VerdictID, c.TenantID, c.PlayerUUID, int64(pb.Action_ACTION_BAN), false, true, store.StatusConfirmed).Scan(&n); err != nil {
			return mapErr(err)
		}
		if n == 0 {
			return nil
		}
		if _, err := s.exec(ctx, tx, `INSERT INTO reputation_contrib (tenant_id, player_uuid, verdict_id, weight, created_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT (tenant_id, player_uuid, verdict_id) DO NOTHING`,
			c.TenantID, c.PlayerUUID, c.VerdictID, c.Weight, orNow(c.CreatedAt).UnixMicro()); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}
