package store

import (
	"context"
	"time"
)

// ErasureStore runs the SQL part of GDPR erasure jobs (5.13) and keeps their bookkeeping.
type ErasureStore interface {
	// BeginErasure records (or restarts) the erasure job for player.
	BeginErasure(ctx context.Context, tenantID, player string, at time.Time) error
	// EraseSubject deletes the player's rows in one transaction: players, player_names, appeals,
	// labels, reputation_contrib, inactive bans and their verdicts, clips owned by the player or
	// showing it as a subject. Verdicts of active bans are kept minimal (id, uuid, action, checks,
	// score, times; name, reason, evidence, clips and payload cleared) until unbanned. It returns
	// deleted row counts by table ("verdicts_minimised" for kept verdicts).
	EraseSubject(ctx context.Context, tenantID, player string) (map[string]int64, error)
	// CompleteErasure stores the counts and completion time.
	CompleteErasure(ctx context.Context, tenantID, player string, counts map[string]int64, at time.Time) error
	// GetErasure returns the job or ErrNotFound.
	GetErasure(ctx context.Context, tenantID, player string) (*Erasure, error)
	// ListErasures returns jobs requested at or after since, oldest first (tombstone republishing).
	ListErasures(ctx context.Context, tenantID string, since time.Time) ([]Erasure, error)
}
