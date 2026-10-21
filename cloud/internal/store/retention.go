package store

import (
	"context"
	"fmt"
	"time"
)

const day = 24 * time.Hour

// Retention is how long the janitor keeps each kind of row (ARCHITECTURE 5.7).
type Retention struct {
	Clips         time.Duration // unless referenced by an active ban, open appeal or label
	Verdicts      time.Duration // bans kept while active
	Appeals       time.Duration
	AppealContact time.Duration // after the decision
	Labels        time.Duration
	Usage         time.Duration
	ProbeStats    time.Duration
	Audit         time.Duration
}

// DefaultRetention is the documented retention.
var DefaultRetention = Retention{
	Clips:         90 * day,
	Verdicts:      180 * day,
	Appeals:       365 * day,
	AppealContact: 30 * day,
	Labels:        730 * day,
	Usage:         730 * day,
	ProbeStats:    30 * day,
	Audit:         365 * day,
}

// Purge applies retention r to one tenant at time now and returns deleted rows by table. The
// janitor calls it for every tenant (ListTenantIDs) once a day.
func Purge(ctx context.Context, s Store, tenantID string, now time.Time, r Retention) (map[string]int64, error) {
	counts := map[string]int64{}
	steps := []struct {
		table string
		run   func() (int64, error)
	}{
		{"clips", func() (int64, error) { return s.PurgeClips(ctx, tenantID, now.Add(-r.Clips)) }},
		{"verdicts", func() (int64, error) { return s.PurgeVerdicts(ctx, tenantID, now.Add(-r.Verdicts)) }},
		{"appeals", func() (int64, error) {
			return s.PurgeAppeals(ctx, tenantID, now.Add(-r.Appeals), now.Add(-r.AppealContact))
		}},
		{"labels", func() (int64, error) { return s.PurgeLabels(ctx, tenantID, now.Add(-r.Labels)) }},
		{"usage_daily", func() (int64, error) { return s.PurgeUsage(ctx, tenantID, now.Add(-r.Usage)) }},
		{"probe_stats", func() (int64, error) { return s.PurgeProbeStats(ctx, tenantID, now.Add(-r.ProbeStats)) }},
		{"audit_log", func() (int64, error) { return s.PurgeAudit(ctx, tenantID, now.Add(-r.Audit)) }},
	}
	for _, st := range steps {
		n, err := st.run()
		if err != nil {
			return counts, fmt.Errorf("purge %s: %w", st.table, err)
		}
		counts[st.table] = n
	}
	return counts, nil
}
