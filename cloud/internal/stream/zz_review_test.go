package stream

import (
	"context"
	"fmt"
	"sort"
	"sync"
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
func (f *fakeMsg) Ack() error            { *f.acked = append(*f.acked, f.seq); return nil }
func (f *fakeMsg) Subject() string       { return "pe.events.00" }
func (f *fakeMsg) Data() []byte          { return nil }
func (f *fakeMsg) Headers() nats.Header  { return nil }

// A message that the client never saw first (dropped) and that is redelivered after a later one is
// dropped by receive, and the AckAll of the later one acknowledges it unprocessed.
func TestReviewConsumeGapIsAckedUnprocessed(t *testing.T) {
	lctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	lease := &Lease{m: &LeaseManager{t: LeaseTimings{Fence: time.Hour}}, lastRenew: time.Now(), ctx: lctx, cancel: cancel}
	b := &Bus{log: testLogger(t), m: metrics.New()}
	c := &consumeLoop{b: b, e: &Epoch{Name: "events-00-x", Base: "events-00"}, lease: lease, o: ConsumeOptions{}.withDefaults(),
		notify: make(chan struct{}, 1), bySeq: map[uint64]*pendingMsg{}}
	var handled, acked []uint64
	h := func(_ context.Context, m *Msg, done func(error)) { handled = append(handled, m.Seq); done(nil) }
	for _, seq := range []uint64{5, 7, 6} { // 6 was dropped on first delivery, arrives as a redelivery
		c.receive(context.Background(), &fakeMsg{seq: seq, acked: &acked}, h)
	}
	if err := c.flush(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	t.Logf("handled=%v acked(AckAll)=%v LastAcked=%d", handled, acked, c.res.LastAcked)
}

// Real NATS: large messages and a handler that takes some time make the client-side subscription
// exceed its pending limit; dropped messages are then covered by AckAll of later ones.
func TestReviewConsumeSlowConsumerLoss(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	const n, size = 220, 2 << 20
	payload := make([]byte, size)
	var pub []uint64
	for i := 0; i < n; i += 20 {
		msgs := make([]Message, 0, 20)
		for j := i; j < i+20 && j < n; j++ {
			msgs = append(msgs, Message{Subject: EventsSubject(0), Data: payload, MsgID: fmt.Sprintf("big:%d", j)})
		}
		acks, err := b.PublishAll(ctxT(t, 60*time.Second), msgs)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range acks {
			pub = append(pub, a.Sequence)
		}
	}
	lease := acquire(t, b, PartitionLeaseKey(GroupEvents, 0))
	defer lease.Release(context.Background())
	// Start from the beginning of the stream (sequence 1) by pretending a previous epoch acked 0.
	st, _ := b.JS.Stream(ctxT(t, 5*time.Second), StreamEvents)
	if _, err := st.CreateConsumer(ctxT(t, 5*time.Second), jetstream.ConsumerConfig{Durable: "events-00-old", FilterSubject: EventsSubject(0),
		AckPolicy: jetstream.AckAllPolicy, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: 1}); err != nil {
		t.Fatal(err)
	}
	e, err := b.ResetConsumer(ctxT(t, 5*time.Second), PartitionConsumer(GroupEvents, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[uint64]bool{}
	h := func(_ context.Context, m *Msg, done func(error)) {
		time.Sleep(15 * time.Millisecond) // per-batch processing time on the loop
		mu.Lock()
		seen[m.Seq] = true
		mu.Unlock()
		done(nil)
	}
	ctx, stop := context.WithTimeout(context.Background(), 12*time.Second)
	defer stop()
	res, err := b.Consume(ctx, e, lease, h, ConsumeOptions{})
	info, _ := e.Consumer.Info(ctxT(t, 5*time.Second))
	mu.Lock()
	defer mu.Unlock()
	var missing []uint64
	for _, s := range pub {
		if s <= info.AckFloor.Stream && !seen[s] {
			missing = append(missing, s)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	t.Logf("consume err=%v res=%+v handled=%d/%d ackFloor=%d; acked but never handled: %d %v", err, res, len(seen), n, info.AckFloor.Stream, len(missing), missing)
}
