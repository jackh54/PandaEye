package store

import (
	"context"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// APIStore holds the queries only the REST API needs (ARCHITECTURE 5.8).
type APIStore interface {
	// ListVerdictsShadow is ListVerdicts with an extra filter on the shadow flag (nil = any).
	ListVerdictsShadow(ctx context.Context, tenantID string, f VerdictFilter, shadow *bool, page Page) ([]Verdict, string, error)
	// CountShadowVerdicts counts the tenant's shadow verdicts created since `since`.
	CountShadowVerdicts(ctx context.Context, tenantID string, since time.Time) (int64, error)
	// ListPlayers pages through the tenant's players, most recently seen first. A non-empty
	// namePrefix keeps players who used a name starting with it (case-insensitive).
	ListPlayers(ctx context.Context, tenantID, namePrefix string, page Page) ([]Player, string, error)
	// LookupVerdictTenant returns the tenant of a verdict id, or ErrNotFound. Not tenant-scoped:
	// public appeals carry only the verdict id (the appeal code is what authenticates them).
	LookupVerdictTenant(ctx context.Context, verdictID string) (string, error)
	// DeleteVerdictLabels deletes the tenant's labels of a verdict with the given label value (the
	// "cheat" label of a confirmed verdict that is overturned) and returns how many it deleted.
	DeleteVerdictLabels(ctx context.Context, tenantID, verdictID, label string) (int64, error)
	// VerdictCategories returns the category of each of the tenant's verdicts among ids (missing
	// ids are absent from the map).
	VerdictCategories(ctx context.Context, tenantID string, ids []string) (map[string]pb.Category, error)
}
