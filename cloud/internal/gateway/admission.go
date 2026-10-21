package gateway

import (
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// admission holds the connection-level limits of ARCHITECTURE 5.3: licence failures per IPv4 /32
// and IPv6 /64 (then a block), new connections per client IP and Hellos per tenant.
type admission struct {
	o Options

	mu        sync.Mutex
	failures  map[netip.Prefix]*failState
	conns     map[netip.Addr]*rate.Limiter
	hellos    map[string]*rate.Limiter
	upgrades  map[netip.Addr]*rate.Limiter
	open      map[netip.Addr]int
	verifying map[netip.Prefix]int
}

type failState struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

func newAdmission(o Options) *admission {
	return &admission{o: o, failures: map[netip.Prefix]*failState{}, conns: map[netip.Addr]*rate.Limiter{}, hellos: map[string]*rate.Limiter{},
		upgrades: map[netip.Addr]*rate.Limiter{}, open: map[netip.Addr]int{}, verifying: map[netip.Prefix]int{}}
}

// failurePrefix returns the /32 (IPv4) or /64 (IPv6) a client address is limited by.
func failurePrefix(ip netip.Addr) netip.Prefix {
	ip = ip.Unmap()
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, _ := ip.Prefix(bits)
	return p
}

// blocked reports whether the client's prefix is blocked after too many licence failures, and for
// how long.
func (a *admission) blocked(ip netip.Addr, now time.Time) (time.Duration, bool) {
	if !ip.IsValid() {
		return 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.failures[failurePrefix(ip)]
	if f == nil || !now.Before(f.blockedUntil) {
		return 0, false
	}
	return f.blockedUntil.Sub(now), true
}

// failure records a licence failure; the prefix is blocked when it reaches the limit.
func (a *admission) failure(ip netip.Addr, now time.Time) {
	if !ip.IsValid() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := failurePrefix(ip)
	f := a.failures[p]
	if f == nil || now.Sub(f.windowStart) >= a.o.AuthFailureWindow {
		f = &failState{windowStart: now}
		a.failures[p] = f
	}
	if f.count++; f.count >= a.o.AuthFailures {
		f.blockedUntil = now.Add(a.o.AuthBlock)
	}
}

// allowConn takes a new-connection token of the client IP; when none is left it returns the time
// until the next one.
func (a *admission) allowConn(ip netip.Addr, now time.Time) (time.Duration, bool) {
	if !ip.IsValid() {
		return 0, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return take(a.conns, ip.Unmap(), a.o.ConnsPerIPPerMin, now)
}

// enter admits a WebSocket upgrade from ip before anything is allocated for it: an upgrade token
// (unauthenticated clients are bounded before their Hello) and a slot of the per-IP open-connection
// cap. ok == false answers 429; otherwise leave must be called when the connection ends.
func (a *admission) enter(ip netip.Addr, now time.Time) (wait time.Duration, ok bool) {
	if !ip.IsValid() {
		return 0, true
	}
	ip = ip.Unmap()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open[ip] >= a.o.MaxConnsPerIP {
		return 5 * time.Second, false
	}
	if wait, ok := take(a.upgrades, ip, a.o.UpgradesPerIPPerMin, now); !ok {
		return wait, false
	}
	a.open[ip]++
	return 0, true
}

// leave releases the slot enter took.
func (a *admission) leave(ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	ip = ip.Unmap()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.open[ip]--; a.open[ip] <= 0 {
		delete(a.open, ip)
	}
}

// startVerify takes one of the prefix's in-flight licence check slots, so concurrent Hellos cannot
// get past the failure block before their failures are counted; done releases it.
func (a *admission) startVerify(ip netip.Addr) (done func(), ok bool) {
	if !ip.IsValid() {
		return func() {}, true
	}
	p := failurePrefix(ip)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.verifying[p] >= a.o.VerifyPerPrefix {
		return nil, false
	}
	a.verifying[p]++
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.verifying[p]--; a.verifying[p] <= 0 {
			delete(a.verifying, p)
		}
	}, true
}

// allowHello takes a Hello token of the tenant.
func (a *admission) allowHello(tenantID string, now time.Time) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return take(a.hellos, tenantID, a.o.HellosPerTenantPerMin, now)
}

func take[K comparable](m map[K]*rate.Limiter, k K, perMin int, now time.Time) (time.Duration, bool) {
	l := m[k]
	if l == nil {
		l = rate.NewLimiter(rate.Limit(float64(perMin)/60), perMin)
		m[k] = l
	}
	if l.AllowN(now, 1) {
		return 0, true
	}
	missing := 1 - l.TokensAt(now)
	return time.Duration(missing / float64(l.Limit()) * float64(time.Second)), false
}

// prune forgets limiters that are full again and failure records that no longer matter.
func (a *admission) prune(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for p, f := range a.failures {
		if !now.Before(f.blockedUntil) && now.Sub(f.windowStart) >= a.o.AuthFailureWindow {
			delete(a.failures, p)
		}
	}
	pruneFull(a.conns, now)
	pruneFull(a.hellos, now)
	pruneFull(a.upgrades, now)
}

func pruneFull[K comparable](m map[K]*rate.Limiter, now time.Time) {
	for k, l := range m {
		if l.TokensAt(now) >= float64(l.Burst()) {
			delete(m, k)
		}
	}
}
