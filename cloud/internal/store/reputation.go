package store

import "context"

// ReputationStore holds reputation network contributions (5.11).
type ReputationStore interface {
	// AddReputationContrib records a contribution (idempotent per tenant, player and verdict).
	AddReputationContrib(ctx context.Context, c ReputationContrib) error
	// RemoveReputationContrib deletes the tenant's contribution for a verdict (overturn).
	RemoveReputationContrib(ctx context.Context, tenantID, verdictID string) (int64, error)
	// PlayerReputation aggregates, per other tenant, the contributions for player. It reads other
	// tenants' rows by design and returns only aggregates; requestingTenantID is excluded.
	PlayerReputation(ctx context.Context, requestingTenantID, player string) ([]TenantReputation, error)
	// TrustInputs returns what decides whether the tenant is trusted.
	TrustInputs(ctx context.Context, tenantID string) (TrustInputs, error)
}
