package worker

import (
	"sync"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

// Plan limits (ARCHITECTURE 5.5). Only the free plan is specified; other plans are not capped on
// players and get a larger clip budget.
type planLimits struct {
	players      int // concurrent tracked players per tenant (per worker process); 0 = unlimited
	clipsPerHour int
}

var (
	freeLimits = planLimits{players: 60, clipsPerHour: 30}
	paidLimits = planLimits{players: 0, clipsPerHour: 300}
)

func limitsFor(plan string) planLimits {
	if plan == "" || plan == store.PlanFree {
		return freeLimits
	}
	return paidLimits
}

// tenantSource reads tenant settings (KV PE_TENANTS mirror).
type tenantSource interface {
	Get(tenantID string) (stream.TenantSettings, bool)
}

type noTenants struct{}

func (noTenants) Get(string) (stream.TenantSettings, bool) { return stream.TenantSettings{}, false }

// tenantConfig adapts the tenant's current settings to checks.Config; it reads the mirror on every
// call, so a settings change applies to the next event.
type tenantConfig struct {
	src    tenantSource
	tenant string
}

func (c tenantConfig) CheckEnabled(id string, def bool) bool {
	s, ok := c.src.Get(c.tenant)
	if !ok {
		return def
	}
	return s.CheckEnabled(id, def)
}

func (c tenantConfig) CheckParam(id, name string, def float64) float64 {
	s, ok := c.src.Get(c.tenant)
	if !ok {
		return def
	}
	if cs, ok := s.Checks[id]; ok {
		if v, ok := cs.Overrides[name]; ok {
			return v
		}
	}
	return def
}

var _ checks.Config = tenantConfig{}

// budget is the process-wide state accounting: bytes per tenant against PE_WORKER_MAX_MEMORY and
// tracked players per tenant against the plan cap. Partitions report deltas; when the total is over
// the budget, partitions evict from the heaviest tenant first.
type budget struct {
	max int64

	mu      sync.Mutex
	total   int64
	tenants map[string]*tenantUsage
}

type tenantUsage struct {
	bytes   int64
	players int
}

func newBudget(max int64) *budget {
	if max <= 0 {
		max = 2 << 30
	}
	return &budget{max: max, tenants: map[string]*tenantUsage{}}
}

func (b *budget) usage(tenant string) *tenantUsage {
	u := b.tenants[tenant]
	if u == nil {
		u = &tenantUsage{}
		b.tenants[tenant] = u
	}
	return u
}

func (b *budget) add(tenant string, delta int64) {
	if delta == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.usage(tenant)
	u.bytes += delta
	b.total += delta
	b.gc(tenant, u)
}

func (b *budget) gc(tenant string, u *tenantUsage) {
	if u.bytes <= 0 && u.players <= 0 {
		b.total -= u.bytes
		delete(b.tenants, tenant)
	}
}

// track takes a tracked-player slot for the tenant unless the cap (0 = none) is reached.
func (b *budget) track(tenant string, limit int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.usage(tenant)
	if limit > 0 && u.players >= limit {
		return false
	}
	u.players++
	return true
}

func (b *budget) untrack(tenant string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := b.tenants[tenant]; u != nil {
		u.players--
		b.gc(tenant, u)
	}
}

// over returns how many bytes the process is over its budget (0 when within it).
func (b *budget) over() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(0, b.total-b.max)
}

// heaviest returns the tenants by state bytes, largest first.
func (b *budget) heaviest() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.tenants))
	for t := range b.tenants {
		out = append(out, t)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && b.tenants[out[j]].bytes > b.tenants[out[j-1]].bytes; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (b *budget) totals() (bytes int64, players int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, u := range b.tenants {
		players += u.players
	}
	return b.total, players
}

// dedupTable keeps the last batch seq per (tenant, server) of one partition. It outlives consumer
// epochs within the process (never expires); a new owner rebuilds it through the warm start.
type dedupTable struct {
	mu   sync.Mutex
	last map[serverKey]uint64
}

func newDedupTable() *dedupTable { return &dedupTable{last: map[serverKey]uint64{}} }

// admit reports whether batch seq is new for the server and records it.
func (d *dedupTable) admit(k serverKey, seq uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if seq <= d.last[k] {
		return false
	}
	d.last[k] = seq
	return true
}
