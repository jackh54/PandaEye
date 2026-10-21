package stream

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/nats-io/nats.go/jetstream"
)

// LeaseTimings are the lease parameters of ARCHITECTURE 5.4. Safety needs Renew < Fence < TTL: an
// owner fences TTL-Fence before its key can expire, and a new owner waits Settle more before working.
type LeaseTimings struct {
	TTL     time.Duration // PE_LEASES bucket TTL (15 s)
	Renew   time.Duration // renewal period (5 s)
	Fence   time.Duration // self-fence when the last successful renewal is older (10 s)
	Settle  time.Duration // wait after acquiring before working (5 s)
	Request time.Duration // timeout of one KV request (2 s)
}

func (t LeaseTimings) withDefaults() LeaseTimings {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.TTL, 15*time.Second)
	def(&t.Renew, 5*time.Second)
	def(&t.Fence, 10*time.Second)
	def(&t.Settle, 5*time.Second)
	def(&t.Request, 2*time.Second)
	return t
}

// Lease errors; Lease.Err and context.Cause(Lease.Context()) return one of them.
var (
	ErrLeaseHeld = errors.New("stream: lease is held by another owner")
	ErrLeaseLost = errors.New("stream: lease lost to another owner")
	ErrFenced    = errors.New("stream: lease fenced: renewal overdue")
	ErrReleased  = errors.New("stream: lease released")
)

// LeaseManager acquires leases in PE_LEASES for one process. Tokens are "<owner id>#<random epoch>".
type LeaseManager struct {
	kv       jetstream.KeyValue
	ownerID  string
	adoptOwn bool
	t        LeaseTimings
	log      *slog.Logger
	m        *metrics.Metrics
}

func newLeaseManager(kv jetstream.KeyValue, ownerID string, adoptOwn bool, t LeaseTimings, log *slog.Logger, m *metrics.Metrics) *LeaseManager {
	return &LeaseManager{kv: kv, ownerID: ownerID, adoptOwn: adoptOwn, t: t.withDefaults(), log: log, m: m}
}

// OwnerID returns the stable owner id.
func (m *LeaseManager) OwnerID() string { return m.ownerID }

// Timings returns the effective timings.
func (m *LeaseManager) Timings() LeaseTimings { return m.t }

func newEpoch() string { return hex.EncodeToString(identity.RandomKey(8)) }

// ownerOf returns the owner id part of a lease token.
func ownerOf(token []byte) string {
	o, _, _ := strings.Cut(string(token), "#")
	return o
}

// TryAcquire takes the lease on key with kv.Create, or (when adopting own leases, `all` mode) takes
// over a lease whose token carries this process's owner id. ErrLeaseHeld when another owner has it.
// The lease renews itself until Release or loss; work may start only after WaitSettled.
func (m *LeaseManager) TryAcquire(ctx context.Context, key string) (*Lease, error) {
	token := m.ownerID + "#" + newEpoch()
	rctx, cancel := context.WithTimeout(ctx, m.t.Request)
	defer cancel()
	sent := time.Now()
	rev, err := m.kv.Create(rctx, key, []byte(token))
	adopted := false
	if errors.Is(err, jetstream.ErrKeyExists) {
		if !m.adoptOwn {
			return nil, ErrLeaseHeld
		}
		e, gerr := m.kv.Get(rctx, key)
		if gerr != nil || ownerOf(e.Value()) != m.ownerID {
			return nil, ErrLeaseHeld
		}
		sent = time.Now()
		if rev, err = m.kv.Update(rctx, key, []byte(token), e.Revision()); err != nil {
			return nil, ErrLeaseHeld
		}
		adopted = true
	}
	if err != nil {
		return nil, err
	}
	l := &Lease{m: m, key: key, token: token, acquired: sent, rev: rev, lastRenew: sent,
		stop: make(chan struct{}), loopDone: make(chan struct{})}
	l.ctx, l.cancel = context.WithCancelCause(context.Background())
	l.fence = time.AfterFunc(m.t.Fence, func() { l.lose(ErrFenced) })
	m.leaseEvent(key, "acquired")
	m.log.Info("lease acquired", "lease", key, "adopted", adopted)
	go l.run()
	return l, nil
}

// Lease is one held lease.
type Lease struct {
	m        *LeaseManager
	key      string
	token    string
	acquired time.Time

	mu          sync.Mutex
	rev         uint64
	lastRenew   time.Time // send time of the last confirmed write (monotonic clock)
	unconfirmed time.Time // send time of the oldest write whose outcome is unknown
	state       error     // nil while held

	ctx      context.Context
	cancel   context.CancelCauseFunc
	fence    *time.Timer
	stop     chan struct{}
	stopOnce sync.Once
	loopDone chan struct{}
}

// Key returns the lease key.
func (l *Lease) Key() string { return l.key }

// Token returns this lease's owner token.
func (l *Lease) Token() string { return l.token }

// Context is cancelled when the lease is lost, fenced or released; context.Cause tells which.
func (l *Lease) Context() context.Context { return l.ctx }

// Err returns nil while the lease is held, else ErrLeaseLost, ErrFenced or ErrReleased.
func (l *Lease) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// Valid reports whether the lease is held and its last successful renewal is younger than the fence
// interval. Consumers check it before processing and before every ack.
func (l *Lease) Valid() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == nil && time.Since(l.lastRenew) < l.m.t.Fence
}

// WaitSettled blocks until the settle interval after acquisition has passed (a previous owner has
// fenced itself by then). It fails if the lease is lost meanwhile or ctx ends.
func (l *Lease) WaitSettled(ctx context.Context) error {
	t := time.NewTimer(time.Until(l.acquired.Add(l.m.t.Settle)))
	defer t.Stop()
	select {
	case <-t.C:
		if err := l.Err(); err != nil {
			return err
		}
		return nil
	case <-l.ctx.Done():
		return context.Cause(l.ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release stops renewing and deletes the key (revision-checked, so another owner's lease is never
// removed). Release is idempotent.
func (l *Lease) Release(ctx context.Context) error {
	l.stopOnce.Do(func() { close(l.stop) })
	<-l.loopDone
	l.fence.Stop()
	l.mu.Lock()
	prev := l.state
	if prev == nil {
		l.state = ErrReleased
		l.m.leaseEvent(l.key, "released")
	}
	rev := l.rev
	l.mu.Unlock()
	l.cancel(ErrReleased)
	if errors.Is(prev, ErrLeaseLost) || errors.Is(prev, ErrReleased) {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, l.m.t.Request)
	defer cancel()
	err := l.m.kv.Delete(rctx, l.key, jetstream.LastRevision(rev))
	if errors.Is(err, jetstream.ErrKeyRevisionMismatch) || errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil
	}
	return err
}

func (l *Lease) run() {
	defer close(l.loopDone)
	t := time.NewTicker(l.m.t.Renew)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-l.ctx.Done():
			return
		case <-t.C:
			l.renew()
		}
	}
}

// renew writes the token with kv.Update(key, token, lastRevision). When the update fails (timeout,
// revision moved, connection trouble) the stored value decides: our token at a new revision means one
// of our writes landed (adopt the revision; it counts as a renewal at the oldest unconfirmed send
// time), another value or no key means the lease is lost.
func (l *Lease) renew() {
	l.mu.Lock()
	rev := l.rev
	l.mu.Unlock()
	sent := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), l.m.t.Request)
	newRev, err := l.m.kv.Update(ctx, l.key, []byte(l.token), rev)
	cancel()
	if err == nil {
		l.renewed(sent, newRev)
		return
	}
	// Whatever failed, the write may have landed: remember the oldest such attempt.
	l.mu.Lock()
	if l.unconfirmed.IsZero() {
		l.unconfirmed = sent
	}
	l.mu.Unlock()
	ctx, cancel = context.WithTimeout(context.Background(), l.m.t.Request)
	e, gerr := l.m.kv.Get(ctx, l.key)
	cancel()
	switch {
	case gerr == nil && string(e.Value()) == l.token:
		l.mu.Lock()
		landed := l.unconfirmed
		l.mu.Unlock()
		if e.Revision() != rev && !landed.IsZero() {
			// One of our unconfirmed writes landed, no earlier than the oldest one was sent.
			l.renewed(landed, e.Revision())
			return
		}
		l.mu.Lock()
		l.rev = e.Revision()
		l.mu.Unlock()
	case gerr == nil, errors.Is(gerr, jetstream.ErrKeyNotFound), errors.Is(gerr, jetstream.ErrKeyDeleted):
		l.lose(ErrLeaseLost)
	default:
		l.m.log.Warn("lease renewal failed", "lease", l.key, "err", err, "get_err", gerr)
	}
}

func (l *Lease) renewed(sent time.Time, rev uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != nil {
		return
	}
	l.rev, l.unconfirmed = rev, time.Time{}
	if sent.After(l.lastRenew) {
		l.lastRenew = sent
	}
	l.fence.Reset(l.m.t.Fence - time.Since(l.lastRenew))
}

func (l *Lease) lose(cause error) {
	l.mu.Lock()
	if l.state != nil {
		l.mu.Unlock()
		return
	}
	l.state = cause
	l.mu.Unlock()
	l.fence.Stop()
	l.cancel(cause)
	l.m.leaseEvent(l.key, "lost")
	l.m.log.Warn("lease lost", "lease", l.key, "cause", cause)
}

func (m *LeaseManager) leaseEvent(key, event string) {
	g := leaseGroup(key)
	m.m.LeaseChanges.WithLabelValues(g, event).Inc()
	if event == "acquired" {
		m.m.LeasesHeld.WithLabelValues(g).Inc()
	} else {
		m.m.LeasesHeld.WithLabelValues(g).Dec()
	}
}

// leaseGroup is the metrics label of a lease key: its group for partition leases.
func leaseGroup(key string) string {
	g, _, _ := strings.Cut(key, ".")
	return g
}
