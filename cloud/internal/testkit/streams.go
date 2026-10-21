package testkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

// StoredMsg is one message read back from a stream.
type StoredMsg struct {
	stream.Msg
	MsgID string // Nats-Msg-Id
}

// ReadStream returns every message stored in streamName (only those on subject when it is not
// empty), oldest first.
func ReadStream(t testing.TB, bus *stream.Bus, streamName, subject string) []StoredMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := jetstream.OrderedConsumerConfig{DeliverPolicy: jetstream.DeliverAllPolicy}
	if subject != "" {
		cfg.FilterSubjects = []string{subject}
	}
	cons, err := bus.JS.OrderedConsumer(ctx, streamName, cfg)
	if err != nil {
		t.Fatalf("testkit: consumer on %s: %v", streamName, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("testkit: consumer info: %v", err)
	}
	want := int(info.NumPending)
	out := make([]StoredMsg, 0, want)
	for len(out) < want {
		batch, err := cons.Fetch(min(want-len(out), 500), jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			t.Fatalf("testkit: fetch: %v", err)
		}
		n := 0
		for m := range batch.Messages() {
			meta, err := m.Metadata()
			if err != nil {
				t.Fatalf("testkit: metadata: %v", err)
			}
			out = append(out, StoredMsg{Msg: stream.Msg{Seq: meta.Sequence.Stream, Subject: m.Subject(), Data: m.Data(), Header: m.Headers(),
				Time: meta.Timestamp, Delivered: meta.NumDelivered}, MsgID: m.Headers().Get(jetstream.MsgIDHeader)})
			n++
		}
		if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
			t.Fatalf("testkit: fetch: %v", err)
		}
		if n == 0 {
			t.Fatalf("testkit: %s: read %d of %d messages", streamName, len(out), want)
		}
	}
	return out
}

// StoredBatch is a StreamBatch read back from PE_EVENTS.
type StoredBatch struct {
	Partition int
	MsgID     string
	Batch     *pb.StreamBatch
}

// EventBatches decodes every StreamBatch stored in PE_EVENTS, oldest first.
func EventBatches(t testing.TB, bus *stream.Bus) []StoredBatch {
	t.Helper()
	var out []StoredBatch
	for _, m := range ReadStream(t, bus, stream.StreamEvents, "") {
		p, err := stream.PartitionOf(m.Subject)
		if err != nil {
			t.Fatal(err)
		}
		var b pb.StreamBatch
		if err := proto.Unmarshal(m.Data, &b); err != nil {
			t.Fatalf("testkit: %s: %v", m.Subject, err)
		}
		out = append(out, StoredBatch{Partition: p, MsgID: m.MsgID, Batch: &b})
	}
	return out
}

// StreamMessages returns the number of messages stored in a stream.
func StreamMessages(t testing.TB, bus *stream.Bus, streamName string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := bus.JS.Stream(ctx, streamName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

// Eventually polls cond every 20 ms until it holds, failing the test after d.
func Eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
