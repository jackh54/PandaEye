package store

import (
	"context"
	"time"
)

// ProbeStatsStore records per-minute probe RTT histograms by server and source (ProbeSourcePlugin,
// ProbeSourceCloud).
type ProbeStatsStore interface {
	// RecordProbeStats merges h and maxUs into the bucket of (server, source, minute truncated to the
	// minute).
	RecordProbeStats(ctx context.Context, tenantID, serverID, source string, minute time.Time, h ProbeHistogram, maxUs int64) error
	// ListProbeStats returns buckets of source since `since`, oldest first; serverID "" = all servers.
	ListProbeStats(ctx context.Context, tenantID, serverID, source string, since time.Time) ([]ProbeStat, error)
	// ProbeSummary merges the tenant's buckets of source since `since`.
	ProbeSummary(ctx context.Context, tenantID, source string, since time.Time) (ProbeSummary, error)
	// GlobalProbeSummary merges every tenant's buckets of source since `since` (public /status; cache
	// it). Not tenant-scoped; returns aggregates only.
	GlobalProbeSummary(ctx context.Context, source string, since time.Time) (ProbeSummary, error)
	// PurgeProbeStats deletes buckets before `before`.
	PurgeProbeStats(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
