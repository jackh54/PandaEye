package store

import (
	"context"
	"time"
)

// ProbeStatsStore records per-minute probe RTT buckets.
type ProbeStatsStore interface {
	// RecordProbeStats merges b into the bucket of (server, minute truncated to the minute): counts
	// add up, max is the maximum, percentiles are count-weighted means.
	RecordProbeStats(ctx context.Context, tenantID, serverID string, minute time.Time, b ProbeBucket) error
	// ListProbeStats returns buckets since `since`, oldest first; serverID "" means all servers.
	ListProbeStats(ctx context.Context, tenantID, serverID string, since time.Time) ([]ProbeStat, error)
	// ProbeSummary aggregates the tenant's buckets since `since`.
	ProbeSummary(ctx context.Context, tenantID string, since time.Time) (ProbeSummary, error)
	// GlobalProbeSummary aggregates every tenant's buckets since `since` (public /status). Not
	// tenant-scoped; returns aggregates only.
	GlobalProbeSummary(ctx context.Context, since time.Time) (ProbeSummary, error)
	// PurgeProbeStats deletes buckets before `before`.
	PurgeProbeStats(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
