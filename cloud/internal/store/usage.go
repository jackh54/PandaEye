package store

import (
	"context"
	"time"
)

// UsageStore records peak players per day.
type UsageStore interface {
	// RecordPeakPlayers sets usage_daily for the UTC day of `day` to max(existing, peak).
	RecordPeakPlayers(ctx context.Context, tenantID string, day time.Time, peak int) error
	// ListUsage returns days from since's UTC day on, oldest first.
	ListUsage(ctx context.Context, tenantID string, since time.Time) ([]UsageDay, error)
	// PurgeUsage deletes days before `before`.
	PurgeUsage(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
