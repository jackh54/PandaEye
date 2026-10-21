package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Clips (ARCHITECTURE 5.5): per player a ring of the last 30 s of events as encoded bytes; a clip of
// the last 25 s is saved when a signal with points >= 5 is emitted or the player's points of the last
// 60 s reach 10, at most one per player per 60 s and within the tenant's hourly budget. Clips are
// saved asynchronously; the triggering signal is published with clip_id only after SaveClip returned.
const (
	clipRingUs       = 30_000_000
	clipSpanUs       = 25_000_000
	clipPointsSingle = 5.0
	clipPointsWindow = 10.0
	clipPointsSpanUs = 60_000_000
	clipGapUs        = 60_000_000
	clipFormat       = 1
	ringItemOverhead = 64 // bytes accounted per ring item beyond its encoding
)

// clipNamespace makes clip ids deterministic: UUIDv5 of the triggering signal's msg id, so a clip
// re-created after a handover is the same row (SaveClip reports ErrConflict).
var clipNamespace = uuid.MustParse("3a6c1f0e-8d4b-5b2a-9e71-0c5d2f4a6b8e")

func clipID(signalMsgID string) string {
	return uuid.NewSHA1(clipNamespace, []byte(signalMsgID)).String()
}

type ringItem struct {
	t      int64  // normalised
	plugin uint64 // Event.time_us
	raw    []byte
	target identity.UUID // attack target (zero: none)
}

// clipRing keeps the player's events of the last 30 s.
type clipRing struct {
	items []ringItem
	bytes int64
}

func itemCost(raw []byte) int64 { return int64(len(raw)) + ringItemOverhead }

// push appends an event (raw is copied) and drops events older than 30 s; it returns the change in
// accounted bytes.
func (r *clipRing) push(t int64, plugin uint64, raw []byte, target identity.UUID) int64 {
	it := ringItem{t: t, plugin: plugin, raw: append([]byte(nil), raw...), target: target}
	r.items = append(r.items, it)
	delta := itemCost(it.raw)
	r.bytes += delta
	return delta + r.trim(t-clipRingUs)
}

func (r *clipRing) trim(before int64) int64 {
	n := 0
	var freed int64
	for n < len(r.items) && r.items[n].t < before {
		freed += itemCost(r.items[n].raw)
		n++
	}
	if n == 0 {
		return 0
	}
	r.items = append(r.items[:0:0], r.items[n:]...)
	r.bytes -= freed
	return -freed
}

func (r *clipRing) clear() int64 {
	freed := r.bytes
	r.items, r.bytes = nil, 0
	return -freed
}

// since returns the items with t >= from (shared, read-only).
func (r *clipRing) since(from int64) []ringItem {
	i := 0
	for i < len(r.items) && r.items[i].t < from {
		i++
	}
	return r.items[i:len(r.items):len(r.items)]
}

// clipPolicy applies the per-player gap and the per-tenant hourly budget (normalised time).
type clipPolicy struct {
	mu      sync.Mutex
	tenants map[string][]int64
}

func newClipPolicy() *clipPolicy { return &clipPolicy{tenants: map[string][]int64{}} }

func (c *clipPolicy) allow(tenant string, perHour int, p *player, t int64) bool {
	if p.lastClipUs != 0 && t-p.lastClipUs < clipGapUs {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	times := c.tenants[tenant]
	keep := times[:0]
	for _, x := range times {
		if x > t-int64(time.Hour/time.Microsecond) {
			keep = append(keep, x)
		}
	}
	if perHour > 0 && len(keep) >= perHour {
		c.tenants[tenant] = keep
		return false
	}
	c.tenants[tenant] = append(keep, t)
	p.lastClipUs = t
	return true
}

// clipJob is one clip being saved. done is closed when the outcome is known; committed reports
// whether the clip row exists in the store.
type clipJob struct {
	clip      store.Clip
	origin    *pb.Origin
	player    []byte
	startUs   uint64 // plugin clock
	endUs     uint64
	context   [][]byte // the session's PlayerJoin and latest StateChange, before the events
	items     []ringItem
	done      chan struct{}
	committed bool
}

// encode builds the stored payload: a zstd frame of a serialised ClipData (format_version 1). The
// events are appended as raw field-3 entries, exactly as they arrived.
func (j *clipJob) encode() ([]byte, error) {
	b, err := proto.Marshal(&pb.ClipData{Origin: j.origin, Player: j.player, StartUs: j.startUs, EndUs: j.endUs, FormatVersion: clipFormat})
	if err != nil {
		return nil, err
	}
	for _, raw := range j.context {
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, raw)
	}
	for _, it := range j.items {
		b = protowire.AppendTag(b, 3, protowire.BytesType)
		b = protowire.AppendBytes(b, it.raw)
	}
	return codec.CompressZstd(b), nil
}

// clipSaver saves clips through a bounded queue with a few writers.
type clipSaver struct {
	st      store.ClipStore
	log     *slog.Logger
	m       *metrics.Metrics
	results *prometheus.CounterVec
	jobs    chan *clipJob
	timeout time.Duration
	wg      sync.WaitGroup
	stop    chan struct{}
	once    sync.Once
}

const (
	clipQueue   = 64
	clipWriters = 2
)

func newClipSaver(st store.ClipStore, log *slog.Logger, m *metrics.Metrics, results *prometheus.CounterVec) *clipSaver {
	s := &clipSaver{st: st, log: log, m: m, jobs: make(chan *clipJob, clipQueue), timeout: 10 * time.Second,
		stop: make(chan struct{}), results: results}
	for i := 0; i < clipWriters; i++ {
		s.wg.Add(1)
		go s.run()
	}
	return s
}

// submit queues a job; false when the queue is full (the signal goes out without a clip).
func (s *clipSaver) submit(j *clipJob) bool {
	select {
	case <-s.stop:
		return false
	default:
	}
	select {
	case s.jobs <- j:
		return true
	default:
		s.m.Drops.WithLabelValues("clip_queue").Inc()
		s.results.WithLabelValues("queue_full").Inc()
		return false
	}
}

func (s *clipSaver) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stop:
			// Fail what is still queued so no publisher waits for it.
			for {
				select {
				case j := <-s.jobs:
					close(j.done)
				default:
					return
				}
			}
		case j := <-s.jobs:
			s.save(j)
		}
	}
}

func (s *clipSaver) save(j *clipJob) {
	defer close(j.done)
	data, err := j.encode()
	if err != nil {
		s.log.Warn("clip encoding failed", "err", err)
		s.results.WithLabelValues("error").Inc()
		return
	}
	j.clip.Data = data
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	switch err := s.st.SaveClip(ctx, &j.clip); {
	case err == nil:
		j.committed = true
		s.results.WithLabelValues("saved").Inc()
	case errors.Is(err, store.ErrConflict):
		j.committed = true // saved before (same deterministic id, e.g. by the previous partition owner)
		s.results.WithLabelValues("exists").Inc()
	default:
		s.log.Warn("clip save failed", "err", err)
		s.m.Drops.WithLabelValues("clip_save").Inc()
		s.results.WithLabelValues("error").Inc()
	}
}

// close stops the writers after the queued jobs are failed.
func (s *clipSaver) close() {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
}
