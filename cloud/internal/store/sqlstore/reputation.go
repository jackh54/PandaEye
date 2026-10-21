package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) AddReputationContrib(ctx context.Context, c store.ReputationContrib) error {
	_, err := s.exec(ctx, s.w, `INSERT INTO reputation_contrib (tenant_id, player_uuid, verdict_id, weight, created_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (tenant_id, player_uuid, verdict_id) DO NOTHING`,
		c.TenantID, c.PlayerUUID, c.VerdictID, c.Weight, orNow(c.CreatedAt).UnixMicro())
	return err
}

func (s *Store) RemoveReputationContrib(ctx context.Context, tenantID, verdictID string) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM reputation_contrib WHERE tenant_id = ? AND verdict_id = ?`, tenantID, verdictID)
}

func (s *Store) PlayerReputation(ctx context.Context, requestingTenantID, player string) ([]store.TenantReputation, error) {
	rows, err := s.query(ctx, s.r, `SELECT tenant_id, SUM(weight), COUNT(*), MAX(created_at) FROM reputation_contrib
		WHERE player_uuid = ? AND tenant_id <> ? GROUP BY tenant_id ORDER BY tenant_id`, player, requestingTenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.TenantReputation
	for rows.Next() {
		var r store.TenantReputation
		var latest sql.NullInt64
		if err := rows.Scan(&r.TenantID, &r.Weight, &r.Contributions, &latest); err != nil {
			return nil, err
		}
		r.Latest = fromUS(latest)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) TrustInputs(ctx context.Context, tenantID string) (store.TrustInputs, error) {
	var ti store.TrustInputs
	var created int64
	err := s.queryRow(ctx, s.r, `SELECT plan, created_at, reputation_opt_in,
			(SELECT COUNT(*) FROM players p WHERE p.tenant_id = tenants.id)
		FROM tenants WHERE id = ?`, tenantID).Scan(&ti.Plan, &created, &ti.OptIn, &ti.DistinctPlayers)
	if err != nil {
		return ti, mapErr(err)
	}
	ti.CreatedAt = time.UnixMicro(created).UTC()
	return ti, nil
}

func (s *Store) PurgeReputation(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM reputation_contrib WHERE tenant_id = ? AND created_at < ?`, tenantID, before.UnixMicro())
}
