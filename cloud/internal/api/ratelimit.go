package api

import (
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// keyedLimiter is a token bucket per key (API key, dashboard user, client IP), with idle buckets
// pruned.
type keyedLimiter struct {
	limit rate.Limit
	burst int

	mu     sync.Mutex
	m      map[string]*bucket
	pruned time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// newKeyedLimiter allows n events per period per key, all of them at once.
func newKeyedLimiter(n int, period time.Duration) *keyedLimiter {
	return &keyedLimiter{limit: rate.Every(period / time.Duration(n)), burst: n, m: map[string]*bucket{}}
}

// allow takes one token of key at now; when none is left it reports how long until one is.
func (k *keyedLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	idle := time.Duration(float64(k.burst)/float64(k.limit)*float64(time.Second)) + time.Minute
	if now.Sub(k.pruned) > idle || len(k.m) > 100_000 {
		for key, b := range k.m {
			if now.Sub(b.seen) > idle {
				delete(k.m, key)
			}
		}
		k.pruned = now
	}
	b, ok := k.m[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(k.limit, k.burst)}
		k.m[key] = b
	}
	b.seen = now
	r := b.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

// exhausted reports, without taking a token, whether key has no token left at now.
func (k *keyedLimiter) exhausted(key string, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.m[key]
	return ok && b.lim.TokensAt(now) < 1
}

// ipKey buckets IPv6 clients by /64 (one subscriber), IPv4 by address.
func ipKey(a netip.Addr) string {
	if !a.IsValid() {
		return "ip:unknown"
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + a.String()
}
