package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

const (
	warmStartSpan   = 60 * time.Second // replayed silently before the start sequence
	housekeepEvery  = time.Second
	clipWaitMax     = 15 * time.Second // a signal waits this long for its clip, then goes without
	publishBatchMax = 1000             // messages per PublishAll
)

// runPartition owns one leased partition: a new consumer epoch, the warm start, then the ordered
// consume loop whose positions are acked only after every derived message has its PubAck.
func (s *Service) runPartition(ctx context.Context, lease *stream.Lease, p int, en *env) error {
	bus := s.d.Bus
	log := s.d.Log.With("partition", p)
	epoch, err := bus.ResetConsumer(ctx, stream.PartitionConsumer(stream.GroupEvents, p), "")
	if err != nil {
		return err
	}
	eng := newEngine(p, en, s.dedupFor(p))
	part := &partition{eng: eng, log: log}
	defer func() {
		part.mu.Lock()
		eng.close()
		part.mu.Unlock()
	}()
	if epoch.StartSeq > 1 {
		n, err := part.warmStart(ctx, bus, p, epoch.StartSeq-1)
		if err != nil && ctx.Err() == nil {
			log.Warn("warm start incomplete", "err", err, "replayed", n)
		} else {
			log.Debug("warm start done", "replayed", n)
		}
	}
	pub := newPublisher(bus, lease.Context(), log)
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		pub.run()
	}()
	hkCtx, stopHK := context.WithCancel(ctx)
	hkDone := make(chan struct{})
	go func() {
		defer close(hkDone)
		part.housekeeping(hkCtx, pub, en)
	}()
	res, err := bus.Consume(ctx, epoch, lease, part.handler(pub), stream.ConsumeOptions{})
	stopHK()
	<-hkDone
	pub.stop()
	<-pubDone
	log.Debug("partition consumer stopped", "last_applied", res.LastApplied, "last_acked", res.LastAcked, "err", err)
	return err
}

type partition struct {
	mu  sync.Mutex
	eng *engine
	log *slog.Logger
}

// warmStart replays the last 60 s of pe.events.<NN> up to upTo in silent mode: sessions, windows
// and dedup state are rebuilt, nothing is emitted.
func (p *partition) warmStart(ctx context.Context, bus *stream.Bus, part int, upTo uint64) (int, error) {
	return bus.Replay(ctx, stream.ReplaySpec{Stream: stream.StreamEvents, Subject: stream.EventsSubject(part),
		Since: time.Now().Add(-warmStartSpan), UpTo: upTo}, func(m *stream.Msg) error {
		sb, raws, err := decodeBatch(m.Data)
		if err != nil {
			return nil
		}
		p.mu.Lock()
		p.eng.handle(m.Seq, sb, raws, true)
		p.mu.Unlock()
		return nil
	})
}

func decodeBatch(data []byte) (*pb.StreamBatch, [][]byte, error) {
	var sb pb.StreamBatch
	if err := proto.Unmarshal(data, &sb); err != nil {
		return nil, nil, err
	}
	return &sb, splitEvents(data), nil
}

func (p *partition) handler(pub *publisher) stream.Handler {
	return func(_ context.Context, m *stream.Msg, done func(error)) {
		sb, raws, err := decodeBatch(m.Data)
		if err != nil {
			p.eng.env.m.Drops.WithLabelValues("undecodable_batch").Inc()
			p.log.Warn("undecodable event batch", "seq", m.Seq, "err", err)
			done(nil)
			return
		}
		p.mu.Lock()
		out := p.eng.handle(m.Seq, sb, raws, false)
		p.mu.Unlock()
		if len(out) == 0 {
			done(nil)
			return
		}
		pub.enqueue(&unit{msgs: out, done: done})
	}
}

// housekeeping runs the engine's periodic work and enforces the memory budget.
func (p *partition) housekeeping(ctx context.Context, pub *publisher, en *env) {
	t := time.NewTicker(housekeepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := en.now()
		p.mu.Lock()
		out := p.eng.housekeep(now)
		for over := en.budget.over(); over > 0; over = en.budget.over() {
			freed := int64(0)
			for _, tenant := range en.budget.heaviest() {
				if freed = p.eng.shrink(tenant, over, now); freed > 0 {
					break
				}
			}
			p.eng.flush()
			if freed == 0 {
				break
			}
		}
		sessions, _ := p.eng.counts()
		p.mu.Unlock()
		bytes, players := en.budget.totals()
		en.wm.StateBytes.Set(float64(bytes))
		en.wm.Players.Set(float64(players))
		_ = sessions
		if len(out) > 0 {
			pub.enqueue(&unit{msgs: out})
		}
	}
}

// unit is the output of one consumed message (or of housekeeping, without done).
type unit struct {
	msgs []outMsg
	done func(error)
}

// publisher publishes units in order. A unit whose signal waits for a clip holds back the units
// behind it (pe.signals.<NN> stays ordered) for at most clipWaitMax.
type publisher struct {
	bus *stream.Bus
	ctx context.Context
	log *slog.Logger

	mu      sync.Mutex
	queue   []*unit
	wake    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newPublisher(bus *stream.Bus, ctx context.Context, log *slog.Logger) *publisher {
	return &publisher{bus: bus, ctx: ctx, log: log, wake: make(chan struct{}, 1), stopped: make(chan struct{})}
}

func (p *publisher) enqueue(u *unit) {
	p.mu.Lock()
	p.queue = append(p.queue, u)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *publisher) stop() { p.once.Do(func() { close(p.stopped) }) }

var errPublisherStopped = errors.New("worker: publisher stopped")

func (p *publisher) run() {
	defer p.failAll(errPublisherStopped)
	for {
		p.mu.Lock()
		empty := len(p.queue) == 0
		p.mu.Unlock()
		if empty {
			select {
			case <-p.wake:
				continue
			case <-p.stopped:
				return
			case <-p.ctx.Done():
				return
			}
		}
		if !p.waitClips() {
			return
		}
		units, msgs := p.take()
		var err error
		if len(msgs) > 0 {
			_, err = p.bus.PublishAll(p.ctx, msgs)
		}
		for _, u := range units {
			if u.done != nil {
				u.done(err)
			}
		}
		if err != nil {
			if p.ctx.Err() == nil {
				p.log.Warn("signal publish failed", "err", err)
			}
			return
		}
	}
}

// waitClips waits until the head unit's clips are decided (or clipWaitMax passed).
func (p *publisher) waitClips() bool {
	p.mu.Lock()
	head := p.queue[0]
	p.mu.Unlock()
	for _, m := range head.msgs {
		if m.clip == nil {
			continue
		}
		timer := time.NewTimer(clipWaitMax)
		select {
		case <-m.clip.done:
		case <-timer.C:
		case <-p.ctx.Done():
			timer.Stop()
			return false
		}
		timer.Stop()
	}
	return true
}

// take removes the leading units whose clips are decided, up to publishBatchMax messages, and
// encodes their messages (signals get clip_id when their clip was committed).
func (p *publisher) take() ([]*unit, []stream.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var units []*unit
	var msgs []stream.Message
	n := 0
	for n < len(p.queue) {
		u := p.queue[n]
		if n > 0 && (!clipsDecided(u) || len(msgs)+len(u.msgs) > publishBatchMax) {
			break
		}
		for _, m := range u.msgs {
			if m.clip != nil && clipCommitted(m.clip) {
				if sig := m.env.GetSignal(); sig != nil {
					sig.ClipId = m.clip.clip.ID
				}
			}
			data, err := proto.Marshal(m.env)
			if err != nil {
				p.log.Error("signal encoding failed", "msg_id", m.msgID, "err", err)
				continue
			}
			msgs = append(msgs, stream.Message{Subject: m.subject, Data: data, MsgID: m.msgID})
		}
		units = append(units, u)
		n++
	}
	p.queue = append(p.queue[:0:0], p.queue[n:]...)
	return units, msgs
}

func clipsDecided(u *unit) bool {
	for _, m := range u.msgs {
		if m.clip != nil {
			select {
			case <-m.clip.done:
			default:
				return false
			}
		}
	}
	return true
}

func clipCommitted(j *clipJob) bool {
	select {
	case <-j.done:
		return j.committed
	default:
		return false
	}
}

func (p *publisher) failAll(err error) {
	p.mu.Lock()
	q := p.queue
	p.queue = nil
	p.mu.Unlock()
	for _, u := range q {
		if u.done != nil {
			u.done(err)
		}
	}
}
