package stream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Message is one message to publish to a stream with its de-duplication id.
type Message struct {
	Subject string
	Data    []byte
	MsgID   string
}

// ErrPermanent marks publish failures that retrying cannot fix (payload too large, invalid subject,
// rejected by the stream). The gateway answers them with Error{CODE_TOO_LARGE} + ack + drop.
var ErrPermanent = errors.New("stream: permanent publish error")

func classify(err error) error {
	if err == nil {
		return nil
	}
	var api *jetstream.APIError
	switch {
	case errors.Is(err, nats.ErrMaxPayload), errors.Is(err, nats.ErrBadSubject), errors.Is(err, nats.ErrInvalidMsg):
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	case errors.As(err, &api) && api.Code == 400:
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	return err
}

// Publish publishes one message and waits for its PubAck.
func (b *Bus) Publish(ctx context.Context, m Message) (*jetstream.PubAck, error) {
	acks, err := b.PublishAll(ctx, []Message{m})
	if err != nil {
		return nil, err
	}
	return acks[0], nil
}

// PublishAll publishes msgs asynchronously and waits on every PubAckFuture (each bounded by the
// publish timeout). Messages whose publish timed out or found no responders are retried with the
// same msg ids (JetStream de-duplicates them) until ctx is done; the gateway passes a 60 s context.
// A permanent error (ErrPermanent) or ctx's error ends it early. The acks are in msgs order; Duplicate
// is set for messages the stream already held.
func (b *Bus) PublishAll(ctx context.Context, msgs []Message) ([]*jetstream.PubAck, error) {
	acks := make([]*jetstream.PubAck, len(msgs))
	todo := make([]int, len(msgs))
	for i := range msgs {
		todo[i] = i
	}
	backoff := 50 * time.Millisecond
	for {
		futures := make([]jetstream.PubAckFuture, len(todo))
		var lastErr error
		for j, i := range todo {
			m := msgs[i]
			f, err := b.JS.PublishMsgAsync(&nats.Msg{Subject: m.Subject, Data: m.Data}, jetstream.WithMsgID(m.MsgID))
			if err != nil {
				if err = classify(err); errors.Is(err, ErrPermanent) {
					b.m.PublishErrors.WithLabelValues(streamOf(m.Subject)).Inc()
					return nil, err
				}
				lastErr = err
				continue
			}
			futures[j] = f
		}
		var retry []int
		for j, i := range todo {
			f := futures[j]
			if f == nil {
				retry = append(retry, i)
				continue
			}
			select {
			case ack := <-f.Ok():
				acks[i] = ack
			case err := <-f.Err():
				if err = classify(err); errors.Is(err, ErrPermanent) {
					b.m.PublishErrors.WithLabelValues(streamOf(msgs[i].Subject)).Inc()
					return nil, err
				}
				lastErr = err
				retry = append(retry, i)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if len(retry) == 0 {
			return acks, nil
		}
		b.m.PublishErrors.WithLabelValues(streamOf(msgs[retry[0]].Subject)).Inc()
		b.log.Warn("stream publish retry", "messages", len(retry), "err", lastErr)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (last publish error: %v)", ctx.Err(), lastErr)
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, time.Second)
		todo = retry
	}
}

// PublishThen runs PublishAll in the background and calls done with its error, for handlers of the
// partition consumer loop: the loop acks a message only after its derived messages are stored.
func (b *Bus) PublishThen(ctx context.Context, msgs []Message, done func(error)) {
	if len(msgs) == 0 {
		done(nil)
		return
	}
	go func() {
		_, err := b.PublishAll(ctx, msgs)
		done(err)
	}()
}

func streamOf(subject string) string {
	switch {
	case len(subject) > 10 && subject[:10] == "pe.events.":
		return StreamEvents
	case len(subject) > 11 && subject[:11] == "pe.signals.":
		return StreamSignals
	default:
		return StreamVerdicts
	}
}
