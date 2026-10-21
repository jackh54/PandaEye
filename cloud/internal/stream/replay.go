package stream

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ReplaySpec selects messages for Replay.
type ReplaySpec struct {
	Stream  string
	Subject string        // filter, e.g. EventsSubject(7)
	Since   time.Time     // first message time (e.g. now - 60 s for a worker warm start)
	UpTo    uint64        // last stream sequence to deliver, inclusive (e.g. Epoch.StartSeq - 1); 0 = nothing
	Idle    time.Duration // give up when no message arrives for this long (5 s)
}

// Replay delivers, in stream order, the messages on spec.Subject stored since spec.Since with a
// sequence <= spec.UpTo, through an ordered ephemeral consumer (no acks), and stops at UpTo or when
// no older message remains. It is the warm-start (worker, last 60 s of pe.events.<NN>) and rebuild
// (verdict service, last 2 h of pe.signals.<NN>) read before switching to the durable consumer. It
// returns the number of messages passed to fn; an error from fn stops it.
func (b *Bus) Replay(ctx context.Context, spec ReplaySpec, fn func(*Msg) error) (int, error) {
	if spec.UpTo == 0 {
		return 0, nil
	}
	if spec.Idle <= 0 {
		spec.Idle = 5 * time.Second
	}
	since := spec.Since
	cons, err := b.JS.OrderedConsumer(ctx, spec.Stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{spec.Subject},
		DeliverPolicy:  jetstream.DeliverByStartTimePolicy,
		OptStartTime:   &since,
	})
	if err != nil {
		return 0, err
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return 0, err
	}
	if info.NumPending == 0 {
		return 0, nil
	}
	it, err := cons.Messages(jetstream.PullMaxMessages(PullBatch))
	if err != nil {
		return 0, err
	}
	defer it.Stop()
	n := 0
	for {
		nctx, cancel := context.WithTimeout(ctx, spec.Idle)
		m, err := it.Next(jetstream.NextContext(nctx))
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return n, nil
			}
			return n, err
		}
		meta, err := m.Metadata()
		if err != nil {
			return n, err
		}
		seq := meta.Sequence.Stream
		if seq > spec.UpTo {
			return n, nil
		}
		if err := fn(&Msg{Seq: seq, Subject: m.Subject(), Data: m.Data(), Header: m.Headers(), Time: meta.Timestamp, Delivered: meta.NumDelivered}); err != nil {
			return n, err
		}
		n++
		if seq == spec.UpTo || meta.NumPending == 0 {
			return n, nil
		}
	}
}
