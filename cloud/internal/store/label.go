package store

import (
	"context"
	"time"
)

// LabelStore stores ground-truth labels (confirm/overturn/manual).
type LabelStore interface {
	// InsertLabel inserts l (ID generated when empty).
	InsertLabel(ctx context.Context, l *Label) error
	// HasLegitCover reports whether the player has a legit label whose window overlaps [from, to] or
	// an overturned verdict created within [from, to] (KICK/BAN guard, 5.6).
	HasLegitCover(ctx context.Context, tenantID, player string, from, to time.Time) (bool, error)
	// ExportLabels pages through labels created at or after since, oldest first, joined with their
	// verdicts, excluding erased players (ML export).
	ExportLabels(ctx context.Context, tenantID string, since time.Time, page Page) ([]LabelExport, string, error)
	// PurgeLabels deletes labels created before `before`.
	PurgeLabels(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
