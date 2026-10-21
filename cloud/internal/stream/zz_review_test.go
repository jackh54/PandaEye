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

// ResetConsumer deletes the old epochs before creating the new one; if the create fails, the next
// reset finds no consumer and starts at now-2min, skipping the backlog after the old ack floor.
func TestReviewResetConsumerLosesFloor(t *testing.T) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	seqs := publishN(t, b, 0, 10, "old")
	spec := PartitionConsumer(GroupEvents, 0)
	st, _ := b.JS.Stream(ctxT(t, 5*time.Second), StreamEvents)
	if _, err := st.CreateConsumer(ctxT(t, 5*time.Second), jetstream.ConsumerConfig{Durable: spec.Name + "-e1", FilterSubject: spec.Filter,
		AckPolicy: jetstream.AckAllPolicy, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: seqs[0]}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Consumer(ctxT(t, 5*time.Second), spec.Name+"-e1")
	batch, _ := c.Fetch(3)
	var last jetstream.Msg
	for m := range batch.Messages() {
		last = m
	}
	last.DoubleAck(ctxT(t, 5*time.Second)) // ack floor = seqs[2]
	t.Logf("old epoch ack floor: %d (backlog %d..%d unprocessed)", seqs[2], seqs[3], seqs[9])
	time.Sleep(FirstStartBack + 5*time.Second) // the backlog is now older than FirstStartBack
	// A create failure after the deletes (any error: timeout, ctx cancelled at shutdown, crash).
	_, err := b.ResetConsumer(ctxT(t, 5*time.Second), spec, "bad.epoch")
	t.Logf("reset with failing create: %v", err)
	e, err := b.ResetConsumer(ctxT(t, 5*time.Second), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := e.Consumer.Info(ctxT(t, 5*time.Second))
	t.Logf("next epoch: StartSeq=%d StartTime=%v NumPending=%d (want %d pending)", e.StartSeq, !e.StartTime.IsZero(), info.NumPending, 7)
}

func reviewGaps(t *testing.T, opt jetstream.PullConsumeOpt) (handled, gaps int) {
	srv := startServer(t)
	b := openBus(t, testConfig(srv.ClientURL(), "owner-a"))
	payload := make([]byte, 2<<20)
	for i := 0; i < 220; i += 20 {
		var msgs []Message
		for j := i; j < i+20; j++ {
			msgs = append(msgs, Message{Subject: EventsSubject(0), Data: payload, MsgID: fmt.Sprintf("g:%d", j)})
		}
		if _, err := b.PublishAll(ctxT(t, 60*time.Second), msgs); err != nil {
			t.Fatal(err)
		}
	}
	e, err := b.ResetConsumer(ctxT(t, 5*time.Second), PartitionConsumer(GroupEvents, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan jetstream.Msg, PullBatch)
	cc, err := e.Consumer.Consume(func(m jetstream.Msg) { ch <- m }, opt, jetstream.PullPriorityGroup(PriorityGroup))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Stop()
	var last uint64
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-ch:
			time.Sleep(15 * time.Millisecond)
			md, _ := m.Metadata()
			if md.Sequence.Consumer != last+1 {
				gaps++
			}
			last = md.Sequence.Consumer
			handled++
		case <-deadline:
			return
		}
	}
}

func TestReviewPullBytesLimitMitigates(t *testing.T) {
	h1, g1 := reviewGaps(t, jetstream.PullMaxMessages(PullBatch))
	h2, g2 := reviewGaps(t, jetstream.PullMaxMessagesWithBytesLimit(PullBatch, 32<<20))
	t.Logf("PullMaxMessages(100): handled %d, delivery-seq gaps %d; WithBytesLimit(100, 32 MiB): handled %d, gaps %d", h1, g1, h2, g2)
}
