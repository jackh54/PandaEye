package stream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/nats-io/nats.go/jetstream"
)

func TestLeaseContentionAndRelease(t *testing.T) {
	srv := startServer(t)
	a, b := openBus(t, testConfig(srv.ClientURL(), "owner-a")), openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	ctx := ctxT(t, 30*time.Second)
	la, err := a.LeaseManager.TryAcquire(ctx, LeaseWriter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.LeaseManager.TryAcquire(ctx, LeaseWriter); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second owner: %v", err)
	}
	time.Sleep(2 * testTimings.TTL) // renewals keep the lease past the bucket TTL
	if !la.Valid() {
		t.Fatal("renewed lease must stay valid")
	}
	if _, err := b.LeaseManager.TryAcquire(ctx, LeaseWriter); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("lease expired despite renewals: %v", err)
	}
	if err := la.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if la.Valid() || !errors.Is(la.Err(), ErrReleased) || !errors.Is(context.Cause(la.Context()), ErrReleased) {
		t.Fatal("released lease still valid")
	}
	lb, err := b.LeaseManager.TryAcquire(ctx, LeaseWriter)
	if err != nil {
		t.Fatalf("lease must be free right after a graceful release: %v", err)
	}
	if err := la.Release(ctx); err != nil {
		t.Fatal("second release", err)
	}
	if !lb.Valid() {
		t.Fatal("releasing again must not touch the new owner's lease")
	}
}

func TestLeaseSelfFencingBeforeTakeover(t *testing.T) {
	srv := startServer(t)
	a, b := openBus(t, testConfig(srv.ClientURL(), "owner-a")), openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	ctx := ctxT(t, 30*time.Second)
	la, err := a.LeaseManager.TryAcquire(ctx, "events.00")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * testTimings.Renew)
	cut := time.Now()
	a.NC.Close() // owner A can no longer renew (partition, crash of its connection)
	fenced := make(chan time.Time, 1)
	go func() { <-la.Context().Done(); fenced <- time.Now() }()

	var lb *Lease
	for lb == nil {
		if l, err := b.LeaseManager.TryAcquire(ctx, "events.00"); err == nil {
			lb = l
		} else if !errors.Is(err, ErrLeaseHeld) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	takeover := time.Now()
	var fencedAt time.Time
	select {
	case fencedAt = <-fenced:
	default:
	}
	if fencedAt.IsZero() || !errors.Is(la.Err(), ErrFenced) || la.Valid() {
		t.Fatalf("A not fenced before B took over: %v", la.Err())
	}
	if fencedAt.Sub(cut) > testTimings.Fence+50*time.Millisecond {
		t.Fatalf("A fenced %v after its last chance to renew (fence %v)", fencedAt.Sub(cut), testTimings.Fence)
	}
	if margin := takeover.Sub(fencedAt); margin < testTimings.TTL-testTimings.Fence-testTimings.Renew {
		t.Fatalf("takeover only %v after fencing", margin)
	}
	if err := lb.WaitSettled(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseLostToAnotherOwner(t *testing.T) {
	srv := startServer(t)
	a := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	ctx := ctxT(t, 30*time.Second)
	l, err := a.LeaseManager.TryAcquire(ctx, "signals.01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Leases.Put(ctx, "signals.01", []byte("owner-x#123")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Context().Done():
	case <-time.After(5 * testTimings.Renew):
		t.Fatal("overwritten lease not detected")
	}
	if !errors.Is(l.Err(), ErrLeaseLost) || l.Valid() {
		t.Fatal(l.Err())
	}
	if err := l.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if e, err := a.Leases.Get(ctx, "signals.01"); err != nil || string(e.Value()) != "owner-x#123" {
		t.Fatal("release of a lost lease deleted the new owner's key")
	}
}

func TestLeaseAdoptOwnInAllMode(t *testing.T) {
	srv := startServer(t)
	cfg := testConfig(srv.ClientURL(), "all-host1")
	cfg.AdoptOwnLeases = true
	first := openBus(t, cfg)
	ctx := ctxT(t, 30*time.Second)
	old, err := first.LeaseManager.TryAcquire(ctx, LeaseWriter)
	if err != nil {
		t.Fatal(err)
	}
	first.NC.Close() // crash: the key stays until its TTL

	other := testConfig(srv.ClientURL(), "all-host2")
	other.AdoptOwnLeases = true
	if _, err := openBus(t, other).LeaseManager.TryAcquire(ctx, LeaseWriter); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("foreign owner adopted: %v", err)
	}
	noAdopt := testConfig(srv.ClientURL(), "all-host1")
	if _, err := openBus(t, noAdopt).LeaseManager.TryAcquire(ctx, LeaseWriter); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("adopted without AdoptOwnLeases: %v", err)
	}
	start := time.Now()
	restarted := openBus(t, cfg)
	l, err := restarted.LeaseManager.TryAcquire(ctx, LeaseWriter)
	if err != nil {
		t.Fatalf("restart must adopt its own lease: %v", err)
	}
	if time.Since(start) > testTimings.TTL/2 || l.Token() == old.Token() {
		t.Fatal("adoption waited for the TTL or reused the old token")
	}
	time.Sleep(2 * testTimings.TTL)
	if !l.Valid() {
		t.Fatal("adopted lease not renewed")
	}
}

// replyLostKV makes every Update land but report a timeout (the reply got lost), or, with drop set,
// fail without writing.
type replyLostKV struct {
	jetstream.KeyValue
	lose, drop atomic.Bool
}

func (k *replyLostKV) Update(ctx context.Context, key string, value []byte, rev uint64) (uint64, error) {
	if k.drop.Load() {
		return 0, context.DeadlineExceeded
	}
	n, err := k.KeyValue.Update(ctx, key, value, rev)
	if err == nil && k.lose.Load() {
		return 0, context.DeadlineExceeded
	}
	return n, err
}

func TestLeaseAdoptsRevisionAfterUpdateTimeout(t *testing.T) {
	srv := startServer(t)
	a := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	ctx := ctxT(t, 30*time.Second)
	kv := &replyLostKV{KeyValue: a.Leases}
	m := newLeaseManager(kv, "owner-a", false, testTimings, testLogger(t), metrics.New())
	l, err := m.TryAcquire(ctx, LeaseJanitor)
	if err != nil {
		t.Fatal(err)
	}
	kv.lose.Store(true)
	time.Sleep(3 * testTimings.Fence)
	if !l.Valid() {
		t.Fatalf("lease fenced although every renewal landed: %v", l.Err())
	}
	e, _ := a.Leases.Get(ctx, LeaseJanitor)
	l.mu.Lock()
	rev := l.rev
	l.mu.Unlock()
	if e.Revision() != rev || string(e.Value()) != l.Token() {
		t.Fatalf("revision not adopted: stored %d, lease %d", e.Revision(), rev)
	}
	kv.lose.Store(false)
	kv.drop.Store(true)
	select {
	case <-l.Context().Done():
	case <-time.After(testTimings.Fence + time.Second):
		t.Fatal("lease not fenced when renewals stopped landing")
	}
	if !errors.Is(l.Err(), ErrFenced) {
		t.Fatal(l.Err())
	}
}

// partitionTracker fails the test if two owners run the same partition at once.
type partitionTracker struct {
	t       *testing.T
	mu      sync.Mutex
	running map[int]string
}

func (p *partitionTracker) fn(owner string) PartitionFunc {
	return func(ctx context.Context, l *Lease, partition int) error {
		p.mu.Lock()
		if cur, ok := p.running[partition]; ok {
			p.t.Errorf("partition %d run by %s and %s at once", partition, cur, owner)
		}
		p.running[partition] = owner
		p.mu.Unlock()
		<-ctx.Done()
		p.mu.Lock()
		delete(p.running, partition)
		p.mu.Unlock()
		return nil
	}
}

func (p *partitionTracker) count(owner string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, o := range p.running {
		if o == owner {
			n++
		}
	}
	return n
}

func TestRunPartitionsAutoRebalances(t *testing.T) {
	srv := startServer(t)
	a, b := openBus(t, testConfig(srv.ClientURL(), "owner-a")), openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	tr := &partitionTracker{t: t, running: map[int]string{}}
	auto := config.LeaseSelection{Mode: config.LeaseAuto}
	ctxA, cancelA := context.WithCancel(context.Background())
	doneA := make(chan error, 1)
	go func() { doneA <- a.RunPartitions(ctxA, GroupEvents, auto, tr.fn("a")) }()
	eventually(t, 10*time.Second, "a holds all partitions", func() bool { return tr.count("a") == 4 })

	ctxB, cancelB := context.WithCancel(context.Background())
	doneB := make(chan error, 1)
	go func() { doneB <- b.RunPartitions(ctxB, GroupEvents, auto, tr.fn("b")) }()
	eventually(t, 15*time.Second, "fair split", func() bool { return tr.count("a") == 2 && tr.count("b") == 2 })

	cancelA()
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "b takes over everything", func() bool { return tr.count("b") == 4 })
	cancelB()
	<-doneB
	ctx := ctxT(t, 10*time.Second)
	keys, _ := a.Leases.ListKeysFiltered(ctx, "events.*")
	for k := range keys.Keys() {
		t.Errorf("lease %s left after shutdown", k)
	}
}

func TestRunPartitionsListAndAll(t *testing.T) {
	srv := startServer(t)
	a, b := openBus(t, testConfig(srv.ClientURL(), "owner-a")), openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	tr := &partitionTracker{t: t, running: map[int]string{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.RunPartitions(ctx, GroupSignals, config.LeaseSelection{Mode: config.LeaseList, Partitions: []int{1, 3}}, tr.fn("a"))
	eventually(t, 10*time.Second, "listed partitions", func() bool { return tr.count("a") == 2 })
	go b.RunPartitions(ctx, GroupSignals, config.LeaseSelection{Mode: config.LeaseAll}, tr.fn("b"))
	eventually(t, 10*time.Second, "b takes the rest", func() bool { return tr.count("b") == 2 })
	tr.mu.Lock()
	if tr.running[1] != "a" || tr.running[3] != "a" || tr.running[0] != "b" || tr.running[2] != "b" {
		t.Errorf("assignment %v", tr.running)
	}
	tr.mu.Unlock()
}

func TestRunSingletonFailover(t *testing.T) {
	srv := startServer(t)
	a, b := openBus(t, testConfig(srv.ClientURL(), "owner-a")), openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	var active atomic.Int32
	var runs sync.Map
	fn := func(owner string) func(context.Context, *Lease) error {
		return func(ctx context.Context, l *Lease) error {
			if n := active.Add(1); n > 1 {
				t.Errorf("%d writers at once", n)
			}
			runs.Store(owner, true)
			<-ctx.Done()
			active.Add(-1)
			return nil
		}
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	doneA := make(chan error)
	go func() { doneA <- a.RunSingleton(ctxA, LeaseWriter, fn("a")) }()
	eventually(t, 10*time.Second, "a runs", func() bool { _, ok := runs.Load("a"); return ok })
	go b.RunSingleton(ctxB, LeaseWriter, fn("b"))
	time.Sleep(3 * testTimings.Renew)
	if _, ok := runs.Load("b"); ok {
		t.Fatal("b ran while a held the lease")
	}
	cancelA()
	<-doneA
	eventually(t, 10*time.Second, "b takes over", func() bool { _, ok := runs.Load("b"); return ok })
}
