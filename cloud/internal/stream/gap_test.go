package stream

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type fakeMsg struct {
	jetstream.Msg
	seq   uint64
	acked *[]uint64
}

func (f *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{Sequence: jetstream.SequencePair{Stream: f.seq, Consumer: f.seq}, NumDelivered: 1}, nil
}
func (f *fakeMsg) Ack() error           { *f.acked = append(*f.acked, f.seq); return nil }
func (f *fakeMsg) Subject() string      { return "pe.events.00" }
func (f *fakeMsg) Data() []byte         { return nil }
func (f *fakeMsg) Headers() nats.Header { return nil }

// Delivery 6 was dropped by the client and arrives (as a redelivery) after 7: the loop must stop
// before 7 instead of AckAll-ing 6 unprocessed.
func TestConsumeStopsAtDeliveryGap(t *testing.T) {
	lctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	lease := &Lease{m: &LeaseManager{t: LeaseTimings{Fence: time.Hour}}, lastRenew: time.Now(), ctx: lctx, cancel: cancel}
	b := &Bus{log: testLogger(t), m: metrics.New()}
	c := &consumeLoop{b: b, e: &Epoch{Name: "events-00-x", Base: "events-00"}, lease: lease, o: ConsumeOptions{}.withDefaults(),
		notify: make(chan struct{}, 1), bySeq: map[uint64]*pendingMsg{}}
	var handled, acked []uint64
	h := func(_ context.Context, m *Msg, done func(error)) { handled = append(handled, m.Seq); done(nil) }
	for _, seq := range []uint64{5, 7, 6} {
		c.receive(context.Background(), &fakeMsg{seq: seq, acked: &acked}, h)
	}
	if !errors.Is(c.failure, ErrDeliveryGap) {
		t.Fatalf("failure = %v", c.failure)
	}
	if err := c.flush(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(handled) != 1 || handled[0] != 5 || len(acked) != 1 || acked[0] != 5 {
		t.Fatalf("handled=%v acked=%v", handled, acked)
	}
}

// A reset whose create fails must leave the previous epoch (and its ack floor) in place.
func TestResetConsumerKeepsFloorWhenCreateFails(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, b, 0, 10, "old")
	spec := PartitionConsumer(GroupEvents, 0)
	st, err := b.JS.Stream(ctxT(t, 5*time.Second), StreamEvents)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConsumer(ctxT(t, 5*time.Second), jetstream.ConsumerConfig{Durable: spec.Name + "-e1", FilterSubject: spec.Filter,
		AckPolicy: jetstream.AckAllPolicy, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: seqs[0]}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Consumer(ctxT(t, 5*time.Second), spec.Name+"-e1")
	batch, err := c.Fetch(3)
	if err != nil {
		t.Fatal(err)
	}
	var last jetstream.Msg
	for m := range batch.Messages() {
		last = m
	}
	if err := last.DoubleAck(ctxT(t, 5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResetConsumer(ctxT(t, 5*time.Second), spec, "bad.epoch"); err == nil {
		t.Fatal("an invalid durable name must fail the create")
	}
	e, err := b.ResetConsumer(ctxT(t, 5*time.Second), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.StartSeq != seqs[2]+1 || !e.StartTime.IsZero() {
		t.Fatalf("next epoch starts at %d (time %v), want %d", e.StartSeq, e.StartTime, seqs[2]+1)
	}
	if _, err := st.Consumer(ctxT(t, 5*time.Second), spec.Name+"-e1"); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("old epoch not deleted after a successful reset: %v", err)
	}
}

// An idle partition keeps its pin: pull requests are renewed within PinnedTTL. In production a message
// arriving on an idle partition after PinnedTTL (while the previous 30 s pull was still open) failed
// the loop with ErrPinIDMismatch.
func TestIdleConsumerKeepsItsPin(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past PinnedTTL")
	}
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	lease := acquire(t, b, PartitionLeaseKey(GroupSignals, 0))
	defer lease.Release(context.Background())
	e, err := b.ResetConsumer(ctxT(t, 5*time.Second), PartitionConsumer(GroupSignals, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i, at := range []time.Duration{PinnedTTL + 5*time.Second, 2*PinnedTTL + 10*time.Second} {
			time.Sleep(at - time.Duration(i)*(PinnedTTL+5*time.Second))
			if _, err := b.PublishAll(context.Background(), []Message{{Subject: SignalsSubject(0), Data: []byte("x"), MsgID: fmt.Sprintf("pin:%d", i)}}); err != nil {
				t.Error(err)
			}
		}
	}()
	var handled atomic.Int32
	ctx, stop := context.WithTimeout(context.Background(), 2*PinnedTTL+15*time.Second)
	defer stop()
	_, err = b.Consume(ctx, e, lease, func(_ context.Context, _ *Msg, done func(error)) { handled.Add(1); done(nil) }, ConsumeOptions{})
	if err != nil {
		t.Fatalf("idle consume stopped: %v", err)
	}
	if handled.Load() != 2 {
		t.Fatalf("handled %d messages, want 2", handled.Load())
	}
}
