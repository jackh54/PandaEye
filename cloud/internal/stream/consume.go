package stream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Msg is one delivered stream message.
type Msg struct {
	Seq       uint64 // stream sequence
	Subject   string
	Data      []byte
	Header    nats.Header
	Time      time.Time // stream timestamp
	Delivered uint64    // delivery count, 1 on first delivery
}

// Handler processes messages in stream order, on the loop goroutine; it must not block on slow work.
// It calls done exactly once, possibly later and from another goroutine, when every side effect of
// the message is durable (for example from PublishThen once the derived signals have their PubAcks).
// done(err) with an error stops the loop: nothing from that message on is acked, and the next epoch
// redelivers it. ctx outlives a graceful stop of Consume (until the drain is over) and ends when the
// lease is lost; use it for the message's side effects.
type Handler func(ctx context.Context, m *Msg, done func(error))

// ConsumeOptions tunes Consume; zero values take the defaults.
type ConsumeOptions struct {
	// StartAfter: messages with stream sequence <= StartAfter are acked without calling the handler
	// (pass the LastApplied of this process's previous epoch on the same partition).
	StartAfter      uint64
	AckEvery        int           // ack once this many messages completed since the last ack (100)
	AckInterval     time.Duration // and at least this often (200 ms)
	InProgressAfter time.Duration // send InProgress for messages pending longer than this (10 s)
	DrainTimeout    time.Duration // graceful stop: wait this long for in-flight completions (2 s)
}

func (o ConsumeOptions) withDefaults() ConsumeOptions {
	if o.AckEvery <= 0 {
		o.AckEvery = 100
	}
	if o.AckInterval <= 0 {
		o.AckInterval = 200 * time.Millisecond
	}
	if o.InProgressAfter <= 0 {
		o.InProgressAfter = AckWait / 3
	}
	if o.DrainTimeout <= 0 {
		o.DrainTimeout = 2 * time.Second
	}
	return o
}

// ConsumeResult reports the loop's position when it stopped.
type ConsumeResult struct {
	LastApplied uint64 // highest sequence handed to the handler or skipped
	LastAcked   uint64 // highest sequence acknowledged (AckAll)
}

// ErrConsumerGone is returned when the epoch's consumer was deleted, i.e. another owner reset it.
var ErrConsumerGone = errors.New("stream: consumer deleted by a newer epoch")

type pendingMsg struct {
	seq     uint64
	msg     jetstream.Msg
	touched time.Time // received or last InProgress
	state   int       // 0 running, 1 done, 2 failed
}

// consumeLoop is the state of one Consume call.
type consumeLoop struct {
	b      *Bus
	e      *Epoch
	lease  *Lease
	o      ConsumeOptions
	res    ConsumeResult
	notify chan struct{}

	mu        sync.Mutex
	queue     []*pendingMsg // delivery (= stream) order, not yet acked
	bySeq     map[uint64]*pendingMsg
	completed int
	failure   error
}

// Consume runs the ordered partition loop on an epoch's consumer while the lease is valid: messages
// are handed to h in stream order (redeliveries of in-flight messages and sequences already applied
// are not), and the loop acks (AckAll) the highest sequence below which every message has completed,
// every AckEvery completions or AckInterval. Slow messages get InProgress. It returns
//   - nil after ctx is done: it stops fetching, waits up to DrainTimeout for in-flight completions and
//     acks what completed (confirmed by the server) while the lease is still valid;
//   - the lease's error (ErrLeaseLost, ErrFenced) as soon as the lease is lost, without acking;
//   - the first error passed to a done callback, or ErrConsumerGone.
func (b *Bus) Consume(ctx context.Context, e *Epoch, lease *Lease, h Handler, o ConsumeOptions) (ConsumeResult, error) {
	c := &consumeLoop{b: b, e: e, lease: lease, o: o.withDefaults(), notify: make(chan struct{}, 1), bySeq: map[uint64]*pendingMsg{}}
	return c.run(ctx, h)
}

func (c *consumeLoop) run(ctx context.Context, h Handler) (ConsumeResult, error) {
	msgs := make(chan jetstream.Msg, PullBatch)
	consumeErr := make(chan error, 1)
	stopped := make(chan struct{})
	cc, err := c.e.Consumer.Consume(func(m jetstream.Msg) {
		select {
		case msgs <- m:
		case <-stopped:
		}
	},
		jetstream.PullMaxMessages(PullBatch),
		jetstream.PullPriorityGroup(PriorityGroup),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			select {
			case consumeErr <- err:
			default:
			}
		}))
	if err != nil {
		return c.res, err
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopped)
			cc.Stop()
		})
	}
	defer stop()
	// Handlers work under a context that survives a graceful stop (so drain can wait for their
	// completions) but ends when the lease is lost or the loop returns.
	work, cancelWork := context.WithCancelCause(context.WithoutCancel(ctx))
	defer cancelWork(context.Canceled)
	defer context.AfterFunc(c.lease.Context(), func() { cancelWork(context.Cause(c.lease.Context())) })()

	tick := time.NewTicker(c.o.AckInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.lease.Context().Done():
			return c.res, context.Cause(c.lease.Context())
		case <-ctx.Done():
			stop()
			return c.res, c.drain()
		case err := <-consumeErr:
			switch {
			case errors.Is(err, jetstream.ErrConsumerDeleted), errors.Is(err, jetstream.ErrConsumerNotFound):
				return c.res, ErrConsumerGone
			case errors.Is(err, jetstream.ErrPinIDMismatch):
				return c.res, err
			}
			c.b.log.Debug("consume", "consumer", c.e.Name, "err", err)
		case m := <-msgs:
			if !c.lease.Valid() {
				return c.res, c.leaseErr()
			}
			c.receive(work, m, h)
		case <-c.notify:
			c.mu.Lock()
			due := c.completed >= c.o.AckEvery
			c.mu.Unlock()
			if due {
				if err := c.flush(ctx, false); err != nil {
					return c.res, err
				}
			}
		case <-tick.C:
			if err := c.flush(ctx, false); err != nil {
				return c.res, err
			}
			c.progress()
		}
		c.mu.Lock()
		failure := c.failure
		c.mu.Unlock()
		if failure != nil {
			if err := c.flush(ctx, false); err != nil {
				return c.res, err
			}
			return c.res, failure
		}
	}
}

func (c *consumeLoop) leaseErr() error {
	if err := c.lease.Err(); err != nil {
		return err
	}
	return ErrFenced
}

func (c *consumeLoop) receive(ctx context.Context, m jetstream.Msg, h Handler) {
	meta, err := m.Metadata()
	if err != nil {
		c.b.log.Warn("consume: message without metadata", "consumer", c.e.Name, "err", err)
		return
	}
	seq := meta.Sequence.Stream
	c.b.m.ConsumerLag.WithLabelValues(c.e.Base).Set(float64(meta.NumPending))
	c.mu.Lock()
	if p, ok := c.bySeq[seq]; ok {
		p.msg, p.touched = m, time.Now() // redelivery of an in-flight message
		c.mu.Unlock()
		return
	}
	if seq <= c.res.LastAcked || (len(c.queue) > 0 && seq < c.queue[len(c.queue)-1].seq) {
		c.mu.Unlock()
		if seq <= c.res.LastAcked {
			_ = m.Ack() // already covered by an AckAll; acking again stops redelivery
		}
		return
	}
	p := &pendingMsg{seq: seq, msg: m, touched: time.Now()}
	c.queue = append(c.queue, p)
	c.bySeq[seq] = p
	skip := seq <= c.o.StartAfter || seq <= c.res.LastApplied
	if skip {
		p.state = 1
		c.completed++
	}
	c.mu.Unlock()
	if skip {
		return
	}
	c.res.LastApplied = seq
	h(ctx, &Msg{Seq: seq, Subject: m.Subject(), Data: m.Data(), Header: m.Headers(), Time: meta.Timestamp, Delivered: meta.NumDelivered},
		func(err error) { c.complete(p, err) })
}

func (c *consumeLoop) complete(p *pendingMsg, err error) {
	c.mu.Lock()
	if p.state != 0 {
		c.mu.Unlock()
		return
	}
	if err != nil {
		p.state = 2
		if c.failure == nil {
			c.failure = err
		}
	} else {
		p.state = 1
	}
	c.completed++
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// flush acks the longest completed prefix of the queue. confirm waits for the server's confirmation
// (graceful stop, so a new owner reads an up-to-date ack floor).
func (c *consumeLoop) flush(ctx context.Context, confirm bool) error {
	if !c.lease.Valid() {
		return c.leaseErr()
	}
	c.mu.Lock()
	i := 0
	for i < len(c.queue) && c.queue[i].state == 1 {
		delete(c.bySeq, c.queue[i].seq)
		i++
	}
	if i == 0 {
		c.mu.Unlock()
		return nil
	}
	last := c.queue[i-1]
	c.queue = append(c.queue[:0:0], c.queue[i:]...)
	c.completed = 0
	c.mu.Unlock()
	var err error
	if confirm {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.b.cfg.Lease.Request)
		err = last.msg.DoubleAck(actx)
		cancel()
	} else {
		err = last.msg.Ack()
	}
	if err != nil {
		return err
	}
	c.res.LastAcked = last.seq
	return nil
}

// progress extends the ack wait of messages pending longer than InProgressAfter.
func (c *consumeLoop) progress() {
	if !c.lease.Valid() {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.queue {
		if now.Sub(p.touched) < c.o.InProgressAfter {
			continue
		}
		_ = p.msg.InProgress()
		p.touched = now
	}
}

// drain waits for in-flight completions, then acks what completed, while the lease is valid.
func (c *consumeLoop) drain() error {
	deadline := time.NewTimer(c.o.DrainTimeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		busy := false
		for _, p := range c.queue {
			if p.state == 0 {
				busy = true
				break
			}
		}
		c.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-c.notify:
		case <-deadline.C:
			c.b.log.Warn("consume: drain timed out", "consumer", c.e.Name)
			return c.flush(context.Background(), true)
		case <-c.lease.Context().Done():
			return nil
		}
	}
	if !c.lease.Valid() {
		return nil
	}
	return c.flush(context.Background(), true)
}
