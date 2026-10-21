package store

import (
	"context"
	"time"
)

// VerdictStore persists verdicts, their delivery and acknowledgement.
type VerdictStore interface {
	// UpsertVerdict inserts v unless a verdict with its id exists (verdict ids are deterministic, so a
	// re-processed VerdictRecord is a no-op; delivery/ack/status state is never overwritten). It also
	// links v.ClipIDs for clip retention. created reports whether a row was inserted.
	UpsertVerdict(ctx context.Context, v *Verdict) (created bool, err error)
	// GetVerdict returns the verdict or ErrNotFound.
	GetVerdict(ctx context.Context, tenantID, id string) (*Verdict, error)
	// ListVerdicts pages through verdicts matching f, newest first.
	ListVerdicts(ctx context.Context, tenantID string, f VerdictFilter, page Page) ([]Verdict, string, error)
	// ListVerdictsBySession returns the verdicts of one player session, oldest first (verdict
	// service rebuild: actions already taken this session).
	ListVerdictsBySession(ctx context.Context, tenantID, sessionID string) ([]Verdict, error)
	// PendingVerdicts returns the verdicts to (re-)deliver to q.ServerID (see PendingQuery).
	PendingVerdicts(ctx context.Context, tenantID string, q PendingQuery) ([]Verdict, error)
	// MarkDelivered sets delivered_at for the given verdict ids.
	MarkDelivered(ctx context.Context, tenantID string, ids []string, at time.Time) error
	// ApplyAck stores a VerdictAck: acked_at (first ack), executed (sticky once true), ack note. An
	// executed, non-shadow BAN creates its bans row with the acking server as the executing server;
	// an executed UNBAN marks the superseded ban's unban done. ErrNotFound for unknown verdicts.
	ApplyAck(ctx context.Context, a Ack) (AckResult, error)
	// SetVerdictStatus changes the status to `to` if the current status is one of from (any status
	// when from is empty). ErrNotFound when missing, ErrConflict when the status does not match.
	SetVerdictStatus(ctx context.Context, tenantID, id, to string, from ...string) error
	// VerdictCounts counts verdicts created since `since` by action and category.
	VerdictCounts(ctx context.Context, tenantID string, since time.Time) ([]VerdictCount, error)
	// PurgeVerdicts deletes verdicts created before `before`, except those whose ban is active.
	PurgeVerdicts(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
