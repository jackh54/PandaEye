// Package store defines the relational store (ARCHITECTURE 5.7): the Store interface, composed of one
// small interface per domain, and its model types. internal/store/sqlstore implements it for postgres
// and sqlite with identical semantics.
//
// Tenant isolation: every method takes the tenant id and every query filters by it, so a lookup by id
// of another tenant's row returns ErrNotFound. The only exceptions are lookups that bootstrap
// authentication or serve system-wide jobs, each documented on its method: LookupLicense,
// EnsureLicenseTenant, VerifyAPIKey, ListTenantIDs, GlobalProbeSummary, PlayerReputation (aggregated
// over other tenants by design, 5.11) and the MetaStore.
//
// Times are UTC; a zero time.Time means "not set" (NULL).
package store

import (
	"context"
	"errors"
)

// Errors returned by every implementation (wrapped or as is; test with errors.Is).
var (
	ErrNotFound      = errors.New("store: not found")
	ErrConflict      = errors.New("store: conflict")
	ErrLimitExceeded = errors.New("store: limit exceeded")
	ErrInvalid       = errors.New("store: invalid value") // e.g. evidence that is not JSON
)

// Store is the complete relational store.
type Store interface {
	TenantStore
	LicenseStore
	ServerStore
	PlayerStore
	VerdictStore
	BanStore
	AppealStore
	LabelStore
	ClipStore
	APIKeyStore
	ReputationStore
	UsageStore
	ProbeStatsStore
	AuditStore
	ErasureStore
	MetaStore
	APIStore
	VerdictExtraStore

	// Ping checks the database connection.
	Ping(ctx context.Context) error
	// Close closes the database. Pending writes are committed by their own transactions.
	Close() error
}
