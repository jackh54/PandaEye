package stream

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/nats-io/nats.go/jetstream"
)

// Consumer settings (ARCHITECTURE 5.4).
const (
	AckWait        = 30 * time.Second
	MaxAckPending  = 10000
	PullBatch      = 100
	PinnedTTL      = 15 * time.Second
	PriorityGroup  = "owner"
	FirstStartBack = 2 * time.Minute // a partition without a consumer starts this far back, never DeliverAll
)

// ConsumerSpec names the durable consumer of one leased work unit.
type ConsumerSpec struct {
	Stream  string        // e.g. StreamEvents
	Name    string        // base name, e.g. "events-07"; each epoch's durable is "<Name>-<epoch>"
	Filter  string        // subject filter; empty = the whole stream
	AckWait time.Duration // 0 = AckWait (tests shorten it)
}

// PartitionConsumer returns the spec of a partition's consumer: "events-07" on pe.events.07 for
// GroupEvents, "signals-07" on pe.signals.07 for GroupSignals.
func PartitionConsumer(group string, partition int) ConsumerSpec {
	name := group + "-" + identity.FormatPartition(partition)
	if group == GroupEvents {
		return ConsumerSpec{Stream: StreamEvents, Name: name, Filter: EventsSubject(partition)}
	}
	return ConsumerSpec{Stream: StreamSignals, Name: name, Filter: SignalsSubject(partition)}
}

// Epoch is the consumer created for one lease epoch.
type Epoch struct {
	Consumer  jetstream.Consumer
	Base      string    // ConsumerSpec.Name (metrics label)
	Name      string    // durable name of this epoch
	StartSeq  uint64    // first stream sequence delivered (AckFloor + 1); 0 when starting by time
	StartTime time.Time // set when there was no previous consumer
}

// ResetConsumer starts a new consumer epoch after acquiring a lease (ARCHITECTURE 5.4): it reads the
// AckFloor.Stream of the previous epochs' durable consumers, deletes them, and creates
// "<Name>-<epoch>" delivering from AckFloor + 1; without a previous consumer it delivers from
// now - FirstStartBack. Because every epoch has its own durable name, acks from a fenced previous
// owner can never move the new consumer's ack floor. An empty epoch picks a random one.
func (b *Bus) ResetConsumer(ctx context.Context, spec ConsumerSpec, epoch string) (*Epoch, error) {
	if epoch == "" {
		epoch = newEpoch()
	}
	st, err := b.JS.Stream(ctx, spec.Stream)
	if err != nil {
		return nil, fmt.Errorf("stream: %s: %w", spec.Stream, err)
	}
	var (
		floor uint64
		found bool
		old   []string
	)
	prefix := spec.Name + "-"
	lister := st.ListConsumers(ctx)
	for info := range lister.Info() {
		if !strings.HasPrefix(info.Name, prefix) {
			continue
		}
		found = true
		old = append(old, info.Name)
		if info.AckFloor.Stream > floor {
			floor = info.AckFloor.Stream
		}
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("stream: list consumers of %s: %w", spec.Stream, err)
	}
	for _, name := range old {
		if err := st.DeleteConsumer(ctx, name); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
			return nil, fmt.Errorf("stream: delete consumer %s: %w", name, err)
		}
	}
	e := &Epoch{Base: spec.Name, Name: prefix + epoch}
	cc := jetstream.ConsumerConfig{
		Durable:        e.Name,
		FilterSubject:  spec.Filter,
		AckPolicy:      jetstream.AckAllPolicy,
		AckWait:        cmp.Or(spec.AckWait, AckWait),
		MaxAckPending:  MaxAckPending,
		MaxDeliver:     -1,
		ReplayPolicy:   jetstream.ReplayInstantPolicy,
		PriorityPolicy: jetstream.PriorityPolicyPinned,
		PriorityGroups: []string{PriorityGroup},
		PinnedTTL:      PinnedTTL,
	}
	if found {
		e.StartSeq = floor + 1
		cc.DeliverPolicy, cc.OptStartSeq = jetstream.DeliverByStartSequencePolicy, e.StartSeq
	} else {
		e.StartTime = time.Now().Add(-FirstStartBack)
		cc.DeliverPolicy, cc.OptStartTime = jetstream.DeliverByStartTimePolicy, &e.StartTime
	}
	if e.Consumer, err = st.CreateConsumer(ctx, cc); err != nil {
		return nil, fmt.Errorf("stream: create consumer %s: %w", e.Name, err)
	}
	return e, nil
}
