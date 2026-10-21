package sqlstore

import (
	"context"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) RecordProbeStats(ctx context.Context, tenantID, serverID string, minute time.Time, b store.ProbeBucket) error {
	if b.Count <= 0 {
		return nil
	}
	wavg := func(col string) string {
		return `(probe_stats.` + col + ` * probe_stats.count + excluded.` + col + ` * excluded.count) / (probe_stats.count + excluded.count)`
	}
	_, err := s.exec(ctx, s.w, `INSERT INTO probe_stats (tenant_id, server_id, minute, count, p50_us, p95_us, p99_us, max_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, server_id, minute) DO UPDATE SET
			p50_us = `+wavg("p50_us")+`, p95_us = `+wavg("p95_us")+`, p99_us = `+wavg("p99_us")+`,
			max_us = `+s.greatest("probe_stats.max_us", "excluded.max_us")+`,
			count = probe_stats.count + excluded.count`,
		tenantID, serverID, minute.UTC().Truncate(time.Minute).UnixMicro(), b.Count, b.P50, b.P95, b.P99, b.Max)
	return err
}

func (s *Store) ListProbeStats(ctx context.Context, tenantID, serverID string, since time.Time) ([]store.ProbeStat, error) {
	q := `SELECT server_id, minute, count, p50_us, p95_us, p99_us, max_us FROM probe_stats WHERE tenant_id = ? AND minute >= ?`
	args := []any{tenantID, since.UnixMicro()}
	if serverID != "" {
		q += " AND server_id = ?"
		args = append(args, serverID)
	}
	rows, err := s.query(ctx, s.r, q+" ORDER BY minute, server_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ProbeStat
	for rows.Next() {
		p := store.ProbeStat{TenantID: tenantID}
		var m int64
		if err := rows.Scan(&p.ServerID, &m, &p.Count, &p.P50, &p.P95, &p.P99, &p.Max); err != nil {
			return nil, err
		}
		p.Minute = time.UnixMicro(m).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// probeSummary aggregates buckets; CAST keeps postgres' numeric SUM results integral.
const probeSummary = `SELECT CAST(COALESCE(SUM(count), 0) AS BIGINT),
	CAST(COALESCE(SUM(p50_us * count) / NULLIF(SUM(count), 0), 0) AS BIGINT),
	CAST(COALESCE(SUM(p95_us * count) / NULLIF(SUM(count), 0), 0) AS BIGINT),
	CAST(COALESCE(SUM(p99_us * count) / NULLIF(SUM(count), 0), 0) AS BIGINT),
	CAST(COALESCE(MAX(max_us), 0) AS BIGINT) FROM probe_stats WHERE minute >= ?`

func (s *Store) ProbeSummary(ctx context.Context, tenantID string, since time.Time) (store.ProbeSummary, error) {
	var p store.ProbeSummary
	err := s.queryRow(ctx, s.r, probeSummary+` AND tenant_id = ?`, since.UnixMicro(), tenantID).Scan(&p.Count, &p.P50, &p.P95, &p.P99, &p.Max)
	return p, mapErr(err)
}

func (s *Store) GlobalProbeSummary(ctx context.Context, since time.Time) (store.ProbeSummary, error) {
	var p store.ProbeSummary
	err := s.queryRow(ctx, s.r, probeSummary, since.UnixMicro()).Scan(&p.Count, &p.P50, &p.P95, &p.P99, &p.Max)
	return p, mapErr(err)
}

func (s *Store) PurgeProbeStats(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM probe_stats WHERE tenant_id = ? AND minute < ?`, tenantID, before.UnixMicro())
}
