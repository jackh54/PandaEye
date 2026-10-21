package store

import "context"

// VerdictExtraStore holds the queries only the verdict service's store writer needs (ARCHITECTURE
// 5.6, 5.11).
type VerdictExtraStore interface {
	// AddReputationContribIfConfirmed inserts c only while its verdict (c.TenantID, c.VerdictID) is
	// a BAN of c.PlayerUUID that is non-shadow, executed and confirmed, checked in the same statement
	// (so the api's confirm and the writer's ack cannot both miss it). Idempotent per tenant, player
	// and verdict. It reports whether the contribution exists afterwards.
	AddReputationContribIfConfirmed(ctx context.Context, c ReputationContrib) (bool, error)
}
