package gateway

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"golang.org/x/time/rate"
)

// Reputation lookups (ARCHITECTURE 5.11): budget of 2x the tenant's peak players per hour.
const (
	riskBudgetFactor = 2
	riskMinPeak      = 10   // floor for new tenants without usage yet
	riskMaxPeak      = 5000 // the plugin reports online_players; cap what it can size the budget with
	riskRefresh      = time.Hour
	riskTenantCap    = 1.0 // cap on one tenant's weight
)

// playerJoined records the player and, for tenants in the reputation network, sends PlayerRisk when
// other networks agree the player cheats. It runs after the batch is stored.
func (s *session) playerJoined(player []byte, j *pb.PlayerJoin, at time.Time) {
	u, err := identity.UUIDFromBytes(player)
	if err != nil {
		return
	}
	optIn := s.settings().ReputationOptIn && !j.Bedrock
	name := j.Name
	s.runAux("player", func(ctx context.Context) {
		if err := s.g.d.Store.UpsertPlayer(ctx, s.tenantID, u.String(), name, s.serverID, at); err != nil {
			s.log.Debug("recording a player failed", "err", err)
		}
		if !optIn {
			return
		}
		if !s.g.risk.allow(ctx, s.tenantID, int(s.online.Load()), time.Now()) {
			s.g.m.Drops.WithLabelValues("risk_budget").Inc()
			return
		}
		reps, err := s.g.d.Store.PlayerReputation(ctx, s.tenantID, u.String())
		if err != nil {
			s.log.Warn("reputation lookup failed", "err", err)
			return
		}
		if r := playerRisk(player, reps); r != nil {
			s.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_PlayerRisk{PlayerRisk: r}}, kind: "player_risk"}, false)
		}
	})
}

// playerRisk turns other trusted tenants' contributions into PlayerRisk, or nil when fewer than two
// tenants agree. Each tenant's weight is capped at 1; risk = 1 - 0.5^(sum of capped weights).
func playerRisk(player []byte, reps []store.TenantReputation) *pb.PlayerRisk {
	var sum float64
	var tenants, bans uint32
	for _, r := range reps {
		if r.Weight <= 0 {
			continue
		}
		tenants++
		bans += uint32(r.Contributions)
		sum += min(r.Weight, riskTenantCap)
	}
	if tenants < 2 {
		return nil
	}
	return &pb.PlayerRisk{Player: player, Risk: 1 - math.Pow(0.5, sum), NetworksFlagged: tenants, ConfirmedBans: bans}
}

// riskBudgets holds the per-tenant reputation lookup budgets.
type riskBudgets struct {
	st store.UsageStore
	mu sync.Mutex
	m  map[string]*riskBudget
}

type riskBudget struct {
	lim       *rate.Limiter
	refreshed time.Time
}

func newRiskBudgets(st store.UsageStore) *riskBudgets {
	return &riskBudgets{st: st, m: map[string]*riskBudget{}}
}

// allow takes one lookup from the tenant's budget, sizing it from the tenant's peak players (usage
// of the last day, at least the connection's current count) once an hour.
func (r *riskBudgets) allow(ctx context.Context, tenantID string, online int, now time.Time) bool {
	r.mu.Lock()
	b := r.m[tenantID]
	stale := b == nil || now.Sub(b.refreshed) >= riskRefresh
	r.mu.Unlock()
	if stale {
		peak := max(online, riskMinPeak)
		if days, err := r.st.ListUsage(ctx, tenantID, now.Add(-24*time.Hour)); err == nil {
			for _, d := range days {
				peak = max(peak, d.PeakPlayers)
			}
		}
		per := riskBudgetFactor * min(peak, riskMaxPeak) // usage peaks are plugin-reported too
		r.mu.Lock()
		if b = r.m[tenantID]; b == nil {
			b = &riskBudget{lim: rate.NewLimiter(rate.Limit(float64(per)/riskRefresh.Seconds()), per)}
			r.m[tenantID] = b
		} else {
			b.lim.SetLimitAt(now, rate.Limit(float64(per)/riskRefresh.Seconds()))
			b.lim.SetBurstAt(now, per)
		}
		b.refreshed = now
		r.mu.Unlock()
	}
	return b.lim.AllowN(now, 1)
}

func (r *riskBudgets) prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, b := range r.m {
		if now.Sub(b.refreshed) >= 2*riskRefresh {
			delete(r.m, k)
		}
	}
}
