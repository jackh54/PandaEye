package store

import (
	"context"
	"time"
)

// AppealStore manages ban appeals.
type AppealStore interface {
	// CreateAppeal inserts an open appeal, atomically enforcing lim (open and total appeals for the
	// verdict, appeals for the tenant in the 24 h before a.CreatedAt): ErrLimitExceeded when over. The
	// per-client-IP limit is the API's (not stored).
	CreateAppeal(ctx context.Context, a *Appeal, lim AppealLimits) error
	// GetAppeal returns the appeal or ErrNotFound.
	GetAppeal(ctx context.Context, tenantID, id string) (*Appeal, error)
	// ListAppeals pages through appeals matching f, newest first.
	ListAppeals(ctx context.Context, tenantID string, f AppealFilter, page Page) ([]Appeal, string, error)
	// DecideAppeal moves an open appeal to status (AppealAccepted or AppealDenied). ErrConflict when it
	// is not open.
	DecideAppeal(ctx context.Context, tenantID, id, status, by, note string, at time.Time) (*Appeal, error)
	// PurgeAppeals deletes appeals created before `before` and clears the contact of appeals decided
	// before contactBefore.
	PurgeAppeals(ctx context.Context, tenantID string, before, contactBefore time.Time) (int64, error)
}
