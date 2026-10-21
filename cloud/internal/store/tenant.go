package store

import (
	"context"
	"time"
)

// TenantStore manages tenants, their settings and per-tenant check configuration.
type TenantStore interface {
	// CreateTenant inserts t. ErrConflict when the id or slug exists.
	CreateTenant(ctx context.Context, t *Tenant) error
	// GetTenant returns the tenant or ErrNotFound.
	GetTenant(ctx context.Context, tenantID string) (*Tenant, error)
	// UpdateTenant applies p and returns the updated tenant (ErrNotFound when missing).
	UpdateTenant(ctx context.Context, tenantID string, p TenantPatch, at time.Time) (*Tenant, error)
	// RecordLicenseCheck stores the last successful licence verification: max servers, the time and
	// until when it may be used while the verifier is unreachable.
	RecordLicenseCheck(ctx context.Context, tenantID string, maxServers int, verifiedAt, validUntil time.Time) error
	// SetOverLimitSince records since when the tenant is over its plan limit (zero clears it).
	SetOverLimitSince(ctx context.Context, tenantID string, since time.Time) error
	// ListTenantIDs returns every tenant id (system jobs: janitor, metering). Not tenant-scoped.
	ListTenantIDs(ctx context.Context) ([]string, error)

	// ListTenantChecks returns the tenant's check configuration ordered by check id.
	ListTenantChecks(ctx context.Context, tenantID string) ([]TenantCheck, error)
	// ReplaceTenantChecks replaces the tenant's whole check configuration with checks.
	ReplaceTenantChecks(ctx context.Context, tenantID string, checks []TenantCheck) error
}
