package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) RecordProbeStats(ctx context.Context, tenantID, serverID, source string, minute time.Time, h store.ProbeHistogram, maxUs int64) error {
	if h.Count() == 0 {
		return nil
	}
	m := minute.UTC().Truncate(time.Minute).UnixMicro()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var empty store.ProbeHistogram
		if _, err := s.exec(ctx, tx, `INSERT INTO probe_stats (tenant_id, server_id, minute, source, count, buckets, max_us)
			VALUES (?, ?, ?, ?, 0, ?, 0) ON CONFLICT DO NOTHING`, tenantID, serverID, m, source, jsonText(empty, "[]")); err != nil {
			return err
		}
		var raw string
		var cur int64
		if err := s.queryRow(ctx, tx, `SELECT buckets, max_us FROM probe_stats WHERE tenant_id = ? AND server_id = ? AND minute = ? AND source = ?`+
			s.forUpdate(), tenantID, serverID, m, source).Scan(&raw, &cur); err != nil {
			return mapErr(err)
		}
		var stored store.ProbeHistogram
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return err
		}
		stored.Merge(h)
		_, err := s.exec(ctx, tx, `UPDATE probe_stats SET count = ?, buckets = ?, max_us = ? WHERE tenant_id = ? AND server_id = ? AND minute = ? AND source = ?`,
			int64(stored.Count()), jsonText(stored, "[]"), max(cur, maxUs), tenantID, serverID, m, source)
		return err
	})
}

func (s *Store) ListProbeStats(ctx context.Context, tenantID, serverID, source string, since time.Time) ([]store.ProbeStat, error) {
	q := `SELECT tenant_id, server_id, source, minute, buckets, max_us FROM probe_stats WHERE tenant_id = ? AND source = ? AND minute >= ?`
	args := []any{tenantID, source, since.UnixMicro()}
	if serverID != "" {
		q += " AND server_id = ?"
		args = append(args, serverID)
	}
	return s.probeRows(ctx, q+" ORDER BY minute, server_id", args...)
}

func (s *Store) probeRows(ctx context.Context, q string, args ...any) ([]store.ProbeStat, error) {
	rows, err := s.query(ctx, s.r, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ProbeStat
	for rows.Next() {
		var p store.ProbeStat
		var m int64
		var raw string
		if err := rows.Scan(&p.TenantID, &p.ServerID, &p.Source, &m, &raw, &p.MaxUs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p.Histogram); err != nil {
			return nil, err
		}
		p.Minute = time.UnixMicro(m).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ProbeSummary(ctx context.Context, tenantID, source string, since time.Time) (store.ProbeSummary, error) {
	rows, err := s.probeRows(ctx, `SELECT tenant_id, server_id, source, minute, buckets, max_us FROM probe_stats
		WHERE tenant_id = ? AND source = ? AND minute >= ?`, tenantID, source, since.UnixMicro())
	if err != nil {
		return store.ProbeSummary{}, err
	}
	return store.MergeProbeStats(rows), nil
}

func (s *Store) GlobalProbeSummary(ctx context.Context, source string, since time.Time) (store.ProbeSummary, error) {
	rows, err := s.probeRows(ctx, `SELECT tenant_id, server_id, source, minute, buckets, max_us FROM probe_stats
		WHERE source = ? AND minute >= ? AND count > 0`, source, since.UnixMicro())
	if err != nil {
		return store.ProbeSummary{}, err
	}
	return store.MergeProbeStats(rows), nil
}

func (s *Store) PurgeProbeStats(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM probe_stats WHERE tenant_id = ? AND minute < ?`, tenantID, before.UnixMicro())
}
