package store

import (
	"context"
	"time"
)

// LabelStore stores ground-truth labels (confirm/overturn/manual).
type LabelStore interface {
	// InsertLabel inserts l (ID generated when empty); ErrConflict when its id exists.
	InsertLabel(ctx context.Context, l *Label) error
	// ReplaceLabel inserts l, or replaces the label with its id when that label's source is one of
	// sources (a staff decision taking over a LiteBans label); ErrConflict otherwise.
	ReplaceLabel(ctx context.Context, l *Label, sources ...string) error
	// DeletePlayerLabels deletes the player's labels of the given value and source whose window ends
	// in [endFrom, endTo] (a wrongful unban superseding its ban's labels) and returns how many.
	DeletePlayerLabels(ctx context.Context, tenantID, player, label, source string, endFrom, endTo time.Time) (int64, error)
	// HasPlayerLabel reports whether the player has a label of the given value and source over exactly
	// [windowStart, windowEnd] (a LiteBans ban reported under two network ids labels its window once).
	HasPlayerLabel(ctx context.Context, tenantID, player, label, source string, windowStart, windowEnd time.Time) (bool, error)
	// HasLegitCover reports whether the player has a legit label whose window overlaps [from, to] or
	// an overturned verdict created within [from, to] (KICK/BAN guard, 5.6).
	HasLegitCover(ctx context.Context, tenantID, player string, from, to time.Time) (bool, error)
	// ExportLabels pages through labels created at or after since, oldest first, joined with their
	// verdicts, excluding erased players (ML export).
	ExportLabels(ctx context.Context, tenantID string, since time.Time, page Page) ([]LabelExport, string, error)
	// PurgeLabels deletes labels created before `before`.
	PurgeLabels(ctx context.Context, tenantID string, before time.Time) (int64, error)
}

// ModerationStore stores moderation labels (LiteBans bans and wrongful unbans) and compares them with
// the verdicts (ARCHITECTURE 5.8).
type ModerationStore interface {
	// RecordModerationLabel inserts m unless a row with its key (tenant, network id, source, entry id,
	// kind) exists and returns the stored row; created reports whether m was inserted.
	RecordModerationLabel(ctx context.Context, m *ModerationLabel) (stored *ModerationLabel, created bool, err error)
	// GetModerationLabel returns the row of (tenant, network id, source, entry id, kind) or ErrNotFound.
	GetModerationLabel(ctx context.Context, tenantID, networkID, source, entryID, kind string) (*ModerationLabel, error)
	// MarkModerationLabelApplied sets applied_at and the number of verdicts the label decided.
	MarkModerationLabelApplied(ctx context.Context, tenantID, networkID, source, entryID, kind string, verdicts int, at time.Time) error
	// ModerationAgreement compares the moderation labels of [since, until) with the tenant's ALERT..BAN
	// verdicts (shadow included) within lookback of each ban (see ModerationAgreement).
	ModerationAgreement(ctx context.Context, tenantID string, since, until time.Time, lookback time.Duration) (ModerationAgreement, error)
	// PurgeModerationLabels deletes rows received before `before`.
	PurgeModerationLabels(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
