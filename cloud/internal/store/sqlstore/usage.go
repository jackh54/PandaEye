package sqlstore

import (
	"context"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (s *Store) RecordPeakPlayers(ctx context.Context, tenantID string, day time.Time, peak int) error {
	_, err := s.exec(ctx, s.w, `INSERT INTO usage_daily (tenant_id, day, peak_players) VALUES (?, ?, ?)
		ON CONFLICT (tenant_id, day) DO UPDATE SET peak_players = `+s.greatest("usage_daily.peak_players", "excluded.peak_players"),
		tenantID, utcDay(day).UnixMicro(), peak)
	return err
}

func (s *Store) ListUsage(ctx context.Context, tenantID string, since time.Time) ([]store.UsageDay, error) {
	rows, err := s.query(ctx, s.r, `SELECT day, peak_players FROM usage_daily WHERE tenant_id = ? AND day >= ? ORDER BY day`,
		tenantID, utcDay(since).UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.UsageDay
	for rows.Next() {
		var d int64
		var u store.UsageDay
		if err := rows.Scan(&d, &u.PeakPlayers); err != nil {
			return nil, err
		}
		u.Day = time.UnixMicro(d).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) PurgeUsage(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM usage_daily WHERE tenant_id = ? AND day < ?`, tenantID, utcDay(before).UnixMicro())
}
