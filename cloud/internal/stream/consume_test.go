package stream

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// publishN stores n messages on pe.events.<partition> and returns their stream sequences.
func publishN(t *testing.T, b *Bus, partition, n int, tag string) []uint64 {
	t.Helper()
	msgs := make([]Message, n)
	for i := range msgs {
		msgs[i] = Message{Subject: EventsSubject(partition), Data: []byte(fmt.Sprintf("%s-%d", tag, i)), MsgID: fmt.Sprintf("%s:%d:%d", tag, partition, i)}
	}
	acks, err := b.PublishAll(ctxT(t, 30*time.Second), msgs)
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]uint64, n)
	for i, a := range acks {
		seqs[i] = a.Sequence
	}
	return seqs
}

func ackFloor(t *testing.T, e *Epoch) uint64 {
	t.Helper()
	info, err := e.Consumer.Info(ctxT(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return info.AckFloor.Stream
}

// recorder collects handled sequences.
type recorder struct {
	mu   sync.Mutex
	seqs []uint64
}

func (r *recorder) add(s uint64) {
	r.mu.Lock()
	r.seqs = append(r.seqs, s)
	r.mu.Unlock()
}

func (r *recorder) get() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.seqs...)
}

func acquire(t *testing.T, b *Bus, key string) *Lease {
	t.Helper()
	ctx := ctxT(t, 30*time.Second)
	for {
		l, err := b.LeaseManager.TryAcquire(ctx, key)
		if err == nil {
			if err := l.WaitSettled(ctx); err != nil {
				t.Fatal(err)
			}
			return l
		}
		if !errors.Is(err, ErrLeaseHeld) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConsumeOrderedWithContiguousAcks(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, b, 1, 300, "m")
	l := acquire(t, b, PartitionLeaseKey(GroupEvents, 1))
	e, err := b.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 1), newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	if e.StartSeq != 0 || e.StartTime.IsZero() {
		t.Fatalf("first epoch must start by time: %+v", e)
	}
	gateSeq := seqs[149]
	gate := make(chan struct{})
	var order recorder
	handler := func(ctx context.Context, m *Msg, done func(error)) {
		order.add(m.Seq)
		go func() {
			if m.Seq == gateSeq {
				<-gate
			} else {
				time.Sleep(time.Duration(rand.IntN(3)) * time.Millisecond)
			}
			done(nil)
		}()
	}
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		res ConsumeResult
		err error
	}
	out := make(chan result, 1)
	go func() {
		res, err := b.Consume(ctx, e, l, handler, ConsumeOptions{})
		out <- result{res, err}
	}()
	eventually(t, 10*time.Second, "all messages handled", func() bool { return len(order.get()) == 300 })
	got := order.get()
	for i := range got {
		if got[i] != seqs[i] {
			t.Fatalf("handler order[%d] = %d, want %d", i, got[i], seqs[i])
		}
	}
	time.Sleep(600 * time.Millisecond)
	if f := ackFloor(t, e); f != gateSeq-1 {
		t.Fatalf("ack floor %d, want %d (the highest contiguous completed sequence)", f, gateSeq-1)
	}
	close(gate)
	eventually(t, 5*time.Second, "ack floor reaches the end", func() bool { return ackFloor(t, e) == seqs[299] })
	cancel()
	r := <-out
	if r.err != nil || r.res.LastAcked != seqs[299] || r.res.LastApplied != seqs[299] {
		t.Fatalf("result %+v %v", r.res, r.err)
	}
	e2, err := b.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 1), newEpoch())
	if err != nil || e2.StartSeq != seqs[299]+1 {
		t.Fatalf("next epoch must start after the ack floor: %+v %v", e2, err)
	}
}

func TestEpochFloorWithoutAcks(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	ctx := ctxT(t, 20*time.Second)
	seqs := publishN(t, b, 2, 5, "x")
	spec := PartitionConsumer(GroupEvents, 2)
	e1, err := b.ResetConsumer(ctx, spec, newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	// A consumer that never acked keeps every message: the next epoch starts at the first one.
	e2, err := b.ResetConsumer(ctx, spec, newEpoch())
	if err != nil || e2.StartSeq > seqs[0] {
		t.Fatalf("epoch after an unacked consumer starts at %d, first message %d (%v)", e2.StartSeq, seqs[0], err)
	}
	st, _ := b.JS.Stream(ctx, StreamEvents)
	if _, err := st.Consumer(ctx, e1.Name); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("previous epoch consumer not deleted: %v", err)
	}
	n := 0
	for range st.ListConsumers(ctx).Info() {
		n++
	}
	if n != 1 {
		t.Fatalf("%d consumers left", n)
	}
}

// TestHandoverProcessesInFlightMessages is the scenario the consumer epochs exist for: an owner that
// dies with delivered but unfinished messages must not lose them. With one shared durable consumer
// and AckAll, the next owner's first ack would silently ack them.
func TestHandoverProcessesInFlightMessages(t *testing.T) {
	srv := startServer(t)
	a := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, a, 2, 200, "h")
	key := PartitionLeaseKey(GroupEvents, 2)
	la := acquire(t, a, key)
	ea, err := a.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 2), newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	var aSeen recorder
	go a.Consume(context.Background(), ea, la, func(ctx context.Context, m *Msg, done func(error)) {
		aSeen.add(m.Seq)
		if m.Seq <= seqs[99] {
			done(nil)
		} // the rest stays in flight: the owner crashes before finishing them
	}, ConsumeOptions{})
	eventually(t, 10*time.Second, "A acked its first 100", func() bool { return ackFloor(t, ea) == seqs[99] })
	eventually(t, 10*time.Second, "A received in-flight messages", func() bool { return len(aSeen.get()) > 100 })
	a.NC.Close() // crash: no release, no further acks

	b := openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	lb := acquire(t, b, key)
	eb, err := b.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 2), newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	if eb.StartSeq != seqs[99]+1 {
		t.Fatalf("B starts at %d, want %d", eb.StartSeq, seqs[99]+1)
	}
	var bSeen recorder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Consume(ctx, eb, lb, func(ctx context.Context, m *Msg, done func(error)) { bSeen.add(m.Seq); done(nil) }, ConsumeOptions{})
	eventually(t, 10*time.Second, "B processed the remainder", func() bool { return len(bSeen.get()) >= 100 })
	time.Sleep(300 * time.Millisecond)
	got := bSeen.get()
	if len(got) != 100 {
		t.Fatalf("B processed %d messages, want 100", len(got))
	}
	for i, s := range got {
		if s != seqs[100+i] {
			t.Fatalf("B[%d] = %d, want %d: in-flight messages of A were lost or duplicated", i, s, seqs[100+i])
		}
	}
	eventually(t, 5*time.Second, "B acked everything", func() bool { return ackFloor(t, eb) == seqs[199] })
}

// TestStaleAckCannotMoveNewEpoch: a fenced owner that acks late (e.g. after a long pause) acks its own
// deleted epoch consumer, never the new owner's.
func TestStaleAckCannotMoveNewEpoch(t *testing.T) {
	srv := startServer(t)
	a := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	ctx := ctxT(t, 30*time.Second)
	seqs := publishN(t, a, 3, 50, "s")
	spec := PartitionConsumer(GroupEvents, 3)
	ea, err := a.ResetConsumer(ctx, spec, newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := ea.Consumer.Fetch(50, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var held []jetstream.Msg
	for m := range batch.Messages() {
		held = append(held, m)
	}
	if len(held) != 50 {
		t.Fatalf("fetched %d", len(held))
	}
	b := openBus(t, testConfig(srv.ClientURL(), "owner-b"))
	eb, err := b.ResetConsumer(ctx, spec, newEpoch())
	if err != nil || eb.StartSeq != seqs[0] {
		t.Fatalf("B epoch %+v %v", eb, err)
	}
	_ = held[49].Ack() // A's stale AckAll of everything
	time.Sleep(300 * time.Millisecond)
	if f := ackFloor(t, eb); f >= seqs[0] {
		t.Fatalf("stale ack moved the new epoch's floor to %d", f)
	}
	lb := acquire(t, b, PartitionLeaseKey(GroupEvents, 3))
	var bSeen recorder
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Consume(cctx, eb, lb, func(ctx context.Context, m *Msg, done func(error)) { bSeen.add(m.Seq); done(nil) }, ConsumeOptions{})
	eventually(t, 10*time.Second, "B gets all 50", func() bool { return len(bSeen.get()) == 50 })
}

func TestConsumeStopsImmediatelyOnLeaseLoss(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	key := PartitionLeaseKey(GroupEvents, 0)
	l := acquire(t, b, key)
	e, err := b.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 0), newEpoch())
	if err != nil {
		t.Fatal(err)
	}
	publishN(t, b, 0, 10, "l")
	var seen recorder
	var pendingDone []func(error)
	var mu sync.Mutex
	out := make(chan error, 1)
	go func() {
		_, err := b.Consume(context.Background(), e, l, func(ctx context.Context, m *Msg, done func(error)) {
			seen.add(m.Seq)
			mu.Lock()
			pendingDone = append(pendingDone, done)
			mu.Unlock()
		}, ConsumeOptions{})
		out <- err
	}()
	eventually(t, 10*time.Second, "messages delivered", func() bool { return len(seen.get()) == 10 })
	if _, err := b.Leases.Put(ctxT(t, 5*time.Second), key, []byte("owner-z#1")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-out:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Consume returned %v", err)
		}
	case <-time.After(5 * testTimings.Renew):
		t.Fatal("Consume kept running after the lease was lost")
	}
	mu.Lock()
	for _, d := range pendingDone {
		d(nil) // completions after the loss must not be acked
	}
	mu.Unlock()
	time.Sleep(500 * time.Millisecond)
	if f := ackFloor(t, e); f != 0 {
		t.Fatalf("acked after losing the lease: floor %d", f)
	}
}

func TestConsumeStartAfterAndHandlerError(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, b, 1, 20, "k")
	l := acquire(t, b, PartitionLeaseKey(GroupEvents, 1))
	spec := PartitionConsumer(GroupEvents, 1)
	e, _ := b.ResetConsumer(ctxT(t, 10*time.Second), spec, newEpoch())
	var seen recorder
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan ConsumeResult, 1)
	go func() {
		res, _ := b.Consume(ctx, e, l, func(ctx context.Context, m *Msg, done func(error)) { seen.add(m.Seq); done(nil) }, ConsumeOptions{StartAfter: seqs[9]})
		out <- res
	}()
	eventually(t, 10*time.Second, "ack floor", func() bool { return ackFloor(t, e) == seqs[19] })
	cancel()
	res := <-out
	if got := seen.get(); len(got) != 10 || got[0] != seqs[10] {
		t.Fatalf("handled %v: sequences <= StartAfter must be skipped", got)
	}
	if res.LastAcked != seqs[19] {
		t.Fatalf("result %+v", res)
	}

	more := publishN(t, b, 1, 5, "k2")
	e2, err := b.ResetConsumer(ctxT(t, 10*time.Second), spec, newEpoch())
	if err != nil || e2.StartSeq != seqs[19]+1 {
		t.Fatalf("epoch %+v %v", e2, err)
	}
	boom := errors.New("boom")
	_, err = b.Consume(context.Background(), e2, l, func(ctx context.Context, m *Msg, done func(error)) {
		if m.Seq == more[2] {
			done(boom)
			return
		}
		done(nil)
	}, ConsumeOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("handler error not returned: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if f := ackFloor(t, e2); f != more[1] {
		t.Fatalf("floor %d, want %d (before the failed message)", f, more[1])
	}
}

func TestConsumeGracefulDrain(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, b, 0, 30, "d")
	l := acquire(t, b, PartitionLeaseKey(GroupEvents, 0))
	e, _ := b.ResetConsumer(ctxT(t, 10*time.Second), PartitionConsumer(GroupEvents, 0), newEpoch())
	var seen recorder
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		_, err := b.Consume(ctx, e, l, func(wctx context.Context, m *Msg, done func(error)) {
			seen.add(m.Seq)
			go func() {
				select {
				case <-time.After(300 * time.Millisecond): // slow side effect still finishing at shutdown
					done(nil)
				case <-wctx.Done():
					done(wctx.Err())
				}
			}()
		}, ConsumeOptions{AckInterval: time.Hour, AckEvery: 1000})
		out <- err
	}()
	eventually(t, 10*time.Second, "delivered", func() bool { return len(seen.get()) == 30 })
	cancel()
	if err := <-out; err != nil {
		t.Fatal(err)
	}
	if f := ackFloor(t, e); f != seqs[29] {
		t.Fatalf("graceful stop must wait for in-flight work and ack it: floor %d, want %d", f, seqs[29])
	}
}

func TestReplay(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	ctx := ctxT(t, 30*time.Second)
	since := time.Now().Add(-time.Second)
	var mine []uint64
	for i := 0; i < 5; i++ {
		mine = append(mine, publishN(t, b, 3, 10, fmt.Sprintf("r%d", i))...)
		publishN(t, b, 2, 3, fmt.Sprintf("o%d", i))
	}
	var got []uint64
	n, err := b.Replay(ctx, ReplaySpec{Stream: StreamEvents, Subject: EventsSubject(3), Since: since, UpTo: mine[29]}, func(m *Msg) error {
		if m.Subject != EventsSubject(3) {
			t.Errorf("subject %s", m.Subject)
		}
		got = append(got, m.Seq)
		return nil
	})
	if err != nil || n != 30 {
		t.Fatalf("replay %d %v", n, err)
	}
	for i := range got {
		if got[i] != mine[i] {
			t.Fatalf("replay[%d] = %d, want %d", i, got[i], mine[i])
		}
	}
	start := time.Now()
	if n, err := b.Replay(ctx, ReplaySpec{Stream: StreamEvents, Subject: EventsSubject(3), Since: since, UpTo: 1 << 40}, func(*Msg) error { return nil }); err != nil || n != 50 {
		t.Fatalf("replay to the end: %d %v", n, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("replay must stop when nothing older remains, not wait for the idle timeout")
	}
	for _, spec := range []ReplaySpec{
		{Stream: StreamEvents, Subject: EventsSubject(3), Since: since, UpTo: 0},
		{Stream: StreamEvents, Subject: EventsSubject(0), Since: since, UpTo: 1 << 40},
		{Stream: StreamEvents, Subject: EventsSubject(3), Since: time.Now().Add(time.Hour), UpTo: 1 << 40},
	} {
		if n, err := b.Replay(ctx, spec, func(*Msg) error { return nil }); err != nil || n != 0 {
			t.Fatalf("empty replay %+v: %d %v", spec, n, err)
		}
	}
	stop := errors.New("stop")
	if n, err := b.Replay(ctx, ReplaySpec{Stream: StreamEvents, Subject: EventsSubject(3), Since: since, UpTo: 1 << 40}, func(*Msg) error { return stop }); !errors.Is(err, stop) || n != 0 {
		t.Fatalf("fn error: %d %v", n, err)
	}
}
