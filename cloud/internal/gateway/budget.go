package gateway

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Backpressure (ARCHITECTURE 3, 5.3): budgets are token buckets, and the gateway waits for tokens
// before it processes (and acks) a message, so the socket is not read while over budget. Data is
// never dropped for rate reasons.

// connBudget holds one connection's buckets.
type connBudget struct {
	live, replay, bytes *rate.Limiter
}

func newConnBudget(o Options) connBudget {
	return connBudget{
		live:   rate.NewLimiter(rate.Limit(o.LiveEventsPerS), max(o.LiveEventsBurst, o.MaxBatchEvents)),
		replay: rate.NewLimiter(rate.Limit(o.ReplayEventsPerS), max(o.ReplayEventsPerS, o.MaxBatchEvents)),
		bytes:  rate.NewLimiter(rate.Limit(o.BytesPerS), max(2*o.BytesPerS, 1<<20+1)),
	}
}

// tenantBudgets holds the per-tenant event buckets by plan, shared by every connection of a tenant
// on this gateway.
type tenantBudgets struct {
	plans map[string]int
	mu    sync.Mutex
	m     map[string]*tenantBucket
}

type tenantBucket struct {
	lim  *rate.Limiter
	rate int
}

func newTenantBudgets(plans map[string]int) *tenantBudgets {
	return &tenantBudgets{plans: plans, m: map[string]*tenantBucket{}}
}

// eventsPerS returns the plan's budget (0 = unlimited).
func (t *tenantBudgets) eventsPerS(plan string) int { return t.plans[plan] }

// get returns the tenant's bucket for its current plan, or nil when the plan is unlimited.
func (t *tenantBudgets) get(tenantID, plan string, minBurst int) *rate.Limiter {
	r := t.plans[plan]
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.m[tenantID]
	if r <= 0 {
		delete(t.m, tenantID)
		return nil
	}
	if b == nil {
		b = &tenantBucket{lim: rate.NewLimiter(rate.Limit(r), max(5*r, minBurst)), rate: r}
		t.m[tenantID] = b
	} else if b.rate != r {
		b.lim.SetLimit(rate.Limit(r))
		b.lim.SetBurst(max(5*r, minBurst))
		b.rate = r
	}
	return b.lim
}

func (t *tenantBudgets) prune(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, b := range t.m {
		if b.lim.TokensAt(now) >= float64(b.lim.Burst()) {
			delete(t.m, k)
		}
	}
}

// wait blocks until n tokens of lim are available (in chunks of its burst) and returns how long it
// waited. A nil limiter never waits.
func wait(ctx context.Context, lim *rate.Limiter, n int) (time.Duration, error) {
	if lim == nil || n <= 0 || lim.Limit() == rate.Inf {
		return 0, nil
	}
	start := time.Now()
	for n > 0 {
		k := min(n, lim.Burst())
		if err := lim.WaitN(ctx, k); err != nil {
			return time.Since(start), err
		}
		n -= k
	}
	return time.Since(start), nil
}
