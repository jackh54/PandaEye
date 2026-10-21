package store

import (
	"context"
	"time"
)

// BanStore reads and overturns bans. Ban rows are created only by VerdictStore.ApplyAck.
type BanStore interface {
	// GetBan returns the ban of a verdict or ErrNotFound.
	GetBan(ctx context.Context, tenantID, verdictID string) (*Ban, error)
	// ListBans pages through bans matching f, newest first.
	ListBans(ctx context.Context, tenantID string, f BanFilter, page Page) ([]Ban, string, error)
	// OverturnBan deactivates an active ban and records who overturned it and the UNBAN verdict
	// (unban state pending; none when o.UnbanVerdictID is empty). ErrConflict when not active.
	OverturnBan(ctx context.Context, tenantID, verdictID string, o BanOverturn) error
	// PurgeBans deletes bans that ended before endedBefore: overturned then, or timed bans whose end
	// passed.
	PurgeBans(ctx context.Context, tenantID string, endedBefore time.Time) (int64, error)
}
