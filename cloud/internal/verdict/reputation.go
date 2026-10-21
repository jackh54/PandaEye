package verdict

import (
	"context"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Reputation network rules (ARCHITECTURE 5.11).
const (
	// ReputationWeight is the weight of one contribution (callers of PlayerReputation cap the sum
	// per tenant).
	ReputationWeight = 1.0
	trustMinAge      = 30 * 24 * time.Hour
	trustMinPlayers  = 50
)

// TrustedTenant reports whether a tenant's confirmed bans count for the reputation network: it opted
// in and is on a paid plan, or is older than 30 days with at least 50 distinct players.
func TrustedTenant(ti store.TrustInputs, now time.Time) bool {
	if !ti.OptIn {
		return false
	}
	return ti.Plan != store.PlanFree || (now.Sub(ti.CreatedAt) >= trustMinAge && ti.DistinctPlayers >= trustMinPlayers)
}

// CloudCheck reports whether a check id is computed by the cloud (not a forwarded plugin flag).
func CloudCheck(id string) bool {
	if s, ok := checks.Lookup(id); ok {
		return s.Where == checks.Cloud
	}
	return checks.Family(id) == "ml"
}

// ReputationEligible reports whether a stored verdict can contribute to the reputation network
// once it is executed and confirmed: a non-shadow BAN with at least one cloud-computed check.
func ReputationEligible(v *store.Verdict) bool {
	if v == nil || v.Action != pb.Action_ACTION_BAN || v.Shadow {
		return false
	}
	for _, c := range v.Checks {
		if CloudCheck(c) {
			return true
		}
	}
	return false
}

// ContributeReputation records the reputation contribution of a verdict when every condition of
// 5.11 holds: a non-shadow BAN with a cloud check, executed (ack), confirmed by staff, from an
// opted-in, trusted tenant. It is idempotent and safe to call from both sides of the race: the store
// writer calls it after an executed ack, the api after confirming the verdict (the store re-checks
// executed/confirmed in the same statement that inserts). It reports whether the contribution
// exists afterwards.
func ContributeReputation(ctx context.Context, st store.Store, tenantID, verdictID string, now time.Time) (bool, error) {
	v, err := st.GetVerdict(ctx, tenantID, verdictID)
	if err != nil {
		return false, err
	}
	if !ReputationEligible(v) || !v.Executed || v.Status != store.StatusConfirmed {
		return false, nil
	}
	ti, err := st.TrustInputs(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if !TrustedTenant(ti, now) {
		return false, nil
	}
	return st.AddReputationContribIfConfirmed(ctx, store.ReputationContrib{TenantID: tenantID, PlayerUUID: v.PlayerUUID,
		VerdictID: v.ID, Weight: ReputationWeight, CreatedAt: now})
}
