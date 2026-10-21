package store

import (
	"context"
	"time"
)

// LicenseStore maps licence key HMACs (identity.LicenseKeyHMAC) to tenants.
type LicenseStore interface {
	// LookupLicense finds a licence by key HMAC (ErrNotFound). Not tenant-scoped: it is how a
	// presented licence key finds its tenant.
	LookupLicense(ctx context.Context, keyHMAC []byte) (*License, error)
	// EnsureLicenseTenant returns the tenant of keyHMAC, atomically creating newTenant and the licence
	// row on first use (created = true). The returned licence may be revoked; callers must check
	// RevokedAt. Not tenant-scoped (auth bootstrap).
	EnsureLicenseTenant(ctx context.Context, keyHMAC []byte, newTenant *Tenant, at time.Time) (t *Tenant, l *License, created bool, err error)
	// CreateLicense binds another key to a tenant (rebind). ErrConflict when the key is bound already.
	CreateLicense(ctx context.Context, tenantID string, keyHMAC []byte, at time.Time) error
	// ListLicenses returns the tenant's licences, newest first.
	ListLicenses(ctx context.Context, tenantID string) ([]License, error)
	// RevokeLicense marks a licence revoked (ErrNotFound when it is not the tenant's).
	RevokeLicense(ctx context.Context, tenantID string, keyHMAC []byte, at time.Time) error
}
