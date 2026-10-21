package store

import (
	"context"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
)

// APIKeyStore manages API keys (identity.NewAPIKey).
type APIKeyStore interface {
	// CreateAPIKey inserts k. ErrConflict when the id or hash exists.
	CreateAPIKey(ctx context.Context, k *APIKey) error
	// VerifyAPIKey returns the active key with this id whose secret matches (constant time) and
	// records its use (at most once per minute). Unknown, revoked and wrong keys are all
	// ErrNotFound. Not tenant-scoped: the key determines the tenant.
	VerifyAPIKey(ctx context.Context, id string, secret identity.Secret, now time.Time) (*APIKey, error)
	// ListAPIKeys returns the tenant's keys, newest first.
	ListAPIKeys(ctx context.Context, tenantID string) ([]APIKey, error)
	// RevokeAPIKey revokes a key (ErrNotFound when it is not the tenant's).
	RevokeAPIKey(ctx context.Context, tenantID, id string, at time.Time) error
}
