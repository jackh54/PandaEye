package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
)

// Service is the worker service.
type Service struct {
	d    *app.Deps
	opts Options

	mu    sync.Mutex
	dedup map[int]*dedupTable
}

var _ app.Service = (*Service)(nil)

// Options adjusts a Service (tests).
type Options struct {
	// Now is the worker clock (default time.Now).
	Now func() time.Time
	// Telemetry replaces the process's telemetry sink (sessions and feature windows).
	Telemetry interface {
		telemetry.SessionSink
		telemetry.FeatureSink
	}
	// Scorer replaces the configured ML scorer.
	Scorer ml.Scorer
	// MaxMemory overrides PE_WORKER_MAX_MEMORY.
	MaxMemory int64
}

// New builds the service from the shared dependencies. Dependencies are resolved in Run.
func New(d *app.Deps) (*Service, error) { return NewWithOptions(d, Options{}) }

// NewWithOptions is New with test options.
func NewWithOptions(d *app.Deps, o Options) (*Service, error) {
	return &Service{d: d, opts: o, dedup: map[int]*dedupTable{}}, nil
}

// dedupFor returns the process's batch-seq table of a partition (kept across lease epochs).
func (s *Service) dedupFor(p int) *dedupTable {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.dedup[p]
	if t == nil {
		t = newDedupTable()
		s.dedup[p] = t
	}
	return t
}

// Run consumes the partitions this process leases until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	d := s.d
	if d == nil || d.Bus == nil || d.Config == nil {
		return errors.New("worker: needs the event bus and configuration")
	}
	tenants, err := d.Bus.WatchTenants(ctx)
	if err != nil {
		return fmt.Errorf("worker: watch tenants: %w", err)
	}
	erasures, err := d.Bus.WatchErasures(ctx)
	if err != nil {
		return fmt.Errorf("worker: watch erasures: %w", err)
	}
	for _, ready := range []<-chan struct{}{tenants.Ready(), erasures.Ready()} {
		select {
		case <-ready:
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
			return errors.New("worker: KV mirrors not ready")
		}
	}
	en, err := s.newEnv(tenants, erasures)
	if err != nil {
		return err
	}
	en.saver = newClipSaver(d.Store, d.Log.With("component", "clips"), d.Metrics, en.wm.Clips)
	defer en.saver.close()
	return d.Bus.RunPartitions(ctx, stream.GroupEvents, d.Config.Leases, func(pctx context.Context, lease *stream.Lease, p int) error {
		return s.runPartition(pctx, lease, p, en)
	})
}

// newEnv resolves the process-wide parts of the engines.
func (s *Service) newEnv(tenants tenantSource, erasures erasureSource) (*env, error) {
	d := s.d
	now := s.opts.Now
	if now == nil {
		now = time.Now
	}
	en := &env{
		log: d.Log.With("component", "worker"), m: d.Metrics, wm: newWorkerMetrics(d.Metrics),
		factories: checks.Factories(), tenants: tenants, erasures: erasures, clips: newClipPolicy(),
		features: true, now: now,
	}
	maxMem := s.opts.MaxMemory
	if maxMem == 0 && d.Config != nil {
		maxMem = d.Config.WorkerMaxMemory
	}
	en.budget = newBudget(maxMem)
	if s.opts.Telemetry != nil {
		en.sessions, en.windows = s.opts.Telemetry, s.opts.Telemetry
	} else {
		sink, err := telemetry.New(d)
		if err != nil {
			return nil, fmt.Errorf("worker: telemetry: %w", err)
		}
		en.sessions, en.windows = sink, sink
	}
	if s.opts.Scorer != nil {
		en.scorer = s.opts.Scorer
	} else {
		sc, err := ml.New(d)
		if err != nil {
			return nil, err
		}
		en.scorer = sc
	}
	return en, nil
}
