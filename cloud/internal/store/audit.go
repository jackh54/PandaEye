package store

import (
	"context"
	"time"
)

// AuditStore is the append-only audit log.
type AuditStore interface {
	// InsertAudit appends e (ID generated when empty).
	InsertAudit(ctx context.Context, e *AuditEntry) error
	// ListAudit pages through the tenant's audit log, newest first.
	ListAudit(ctx context.Context, tenantID string, page Page) ([]AuditEntry, string, error)
	// PurgeAudit deletes entries before `before`.
	PurgeAudit(ctx context.Context, tenantID string, before time.Time) (int64, error)
}
