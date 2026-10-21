package verdict

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
)

// RebuildWindow is how far back a new partition owner replays pe.signals.<NN> (rebuild mode).
const RebuildWindow = 2 * time.Hour

const (
	housekeepInterval = time.Minute
	watchReadyTimeout = 30 * time.Second
)

// Service is the verdict service: one partition consumer per leased pe.signals.<NN> partition and
// the leased store writer.
type Service struct {
	d    *app.Deps
	opts options

	// set by Run
	bus      *stream.Bus
	st       store.Store
	log      *slog.Logger
	m        *metrics.Metrics
	sink     telemetry.Sink
	tenants  *stream.Watch[stream.TenantSettings]
	erasures *stream.Erasures
	outKey   []byte
	env      *env
	discord  *discordQueue
	trust    *trustCache
}

var _ app.Service = (*Service)(nil)

// Option configures New (tests).
type Option func(*options)

type options struct {
	now      func() time.Time
	notifier Notifier
	sink     telemetry.Sink
	leases   *config.LeaseSelection
	noWriter bool
	rebuild  time.Duration
	probes   time.Duration // probe_stats flush interval (tests)
}

// WithClock replaces the wall clock.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithNotifier replaces the Discord client.
func WithNotifier(n Notifier) Option { return func(o *options) { o.notifier = n } }

// WithTelemetry replaces the process's telemetry sink.
func WithTelemetry(s telemetry.Sink) Option { return func(o *options) { o.sink = s } }

// WithLeases overrides PE_LEASES for the signal partitions.
func WithLeases(sel config.LeaseSelection) Option { return func(o *options) { o.leases = &sel } }

// WithoutWriter runs only the partition consumers (another instance runs the store writer).
func WithoutWriter() Option { return func(o *options) { o.noWriter = true } }

// WithRebuildWindow replaces RebuildWindow.
func WithRebuildWindow(d time.Duration) Option { return func(o *options) { o.rebuild = d } }

// New builds the service from the shared dependencies. Nothing is connected until Run.
func New(d *app.Deps, opts ...Option) (*Service, error) {
	s := &Service{d: d}
	for _, o := range opts {
		o(&s.opts)
	}
	if s.opts.now == nil {
		s.opts.now = time.Now
	}
	if s.opts.rebuild <= 0 {
		s.opts.rebuild = RebuildWindow
	}
	if s.opts.probes <= 0 {
		s.opts.probes = probeFlushInterval
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.opts.now() }

// Run consumes the leased signal partitions and runs the store writer until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if err := s.init(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil && !errors.Is(err, context.Canceled) {
				errs <- err
			}
		}()
	}
	sel := s.d.Config.Leases
	if s.opts.leases != nil {
		sel = *s.opts.leases
	}
	run(func() error { return s.bus.RunPartitions(ctx, stream.GroupSignals, sel, s.runPartition) })
	if !s.opts.noWriter {
		run(func() error { return s.bus.RunSingleton(ctx, stream.LeaseWriter, s.runWriter) })
	}
	run(func() error { s.discord.run(ctx); return nil })
	run(func() error { s.flushProbes(ctx); return nil })
	wg.Wait()
	close(errs)
	return <-errs
}

func (s *Service) init(ctx context.Context) error {
	d := s.d
	if d.Bus == nil || d.Store == nil || d.Config == nil {
		return errors.New("verdict: needs the configuration, the event bus and the store")
	}
	s.bus, s.st, s.log, s.m = d.Bus, d.Store, d.Log.With("service", "verdict"), d.Metrics
	if s.m == nil {
		s.m = metrics.New()
	}
	s.outKey = d.Secrets.OutboundKey.Bytes()
	if len(s.outKey) == 0 {
		return errors.New("verdict: PE_OUTBOUND_KEY is required")
	}
	var err error
	if s.sink = s.opts.sink; s.sink == nil {
		if s.sink, err = telemetry.New(d); err != nil {
			return err
		}
	}
	if s.tenants, err = s.bus.WatchTenants(ctx); err != nil {
		return err
	}
	if s.erasures, err = s.bus.WatchErasures(ctx); err != nil {
		return err
	}
	for _, ready := range []<-chan struct{}{s.tenants.Ready(), s.erasures.Ready()} {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(watchReadyTimeout):
			return errors.New("verdict: KV watches did not load")
		}
	}
	notifier := s.opts.notifier
	if notifier == nil {
		c, err := discord.Shared(d)
		if err != nil {
			return err
		}
		notifier = c
	}
	s.discord = newDiscordQueue(notifier, d.Secrets.OutboundKeys(), s.log, s.m)
	s.trust = &trustCache{s: s, m: map[string]trustEntry{}}
	s.env = &env{now: s.now, tenant: s.settings, erasedAt: s.erasedAt, store: s.st, signals: s.sink, probes: newProbeStats(),
		outKey: s.outKey, publicURL: d.Config.PublicURL, metrics: s.m, log: s.log}
	return nil
}

// settings returns the tenant's current settings from KV PE_TENANTS, or the defaults.
func (s *Service) settings(tenantID string) stream.TenantSettings {
	if ts, ok := s.tenants.Get(tenantID); ok {
		ts.Leniency = NormalizeLeniency(ts.Leniency)
		return ts
	}
	return defaultSettings(tenantID)
}

func (s *Service) erasedAt(tenantID string, player identity.UUID) (time.Time, bool) {
	return s.erasures.Erased(tenantID, player)
}

// runPartition owns pe.signals.<NN> for one lease: new consumer epoch, rebuild from the last 2 h,
// then the durable consumer.
func (s *Service) runPartition(ctx context.Context, lease *stream.Lease, n int) error {
	e, err := s.bus.ResetConsumer(ctx, stream.PartitionConsumer(stream.GroupSignals, n), "")
	if err != nil {
		return err
	}
	p := newPartition(s.env, n)
	last, replayed, err := s.rebuild(ctx, p, e)
	if err != nil {
		return err
	}
	s.log.Info("partition rebuilt", "partition", n, "messages", replayed, "players", len(p.players))
	em := newEmitter(s)
	defer em.close()
	hctx, stopHK := context.WithCancel(ctx)
	defer stopHK()
	go func() {
		t := time.NewTicker(housekeepInterval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
				p.housekeep()
			}
		}
	}()
	_, err = s.bus.Consume(ctx, e, lease, func(wctx context.Context, m *stream.Msg, done func(error)) {
		fx := p.apply(m, true)
		for _, out := range fx.outbound {
			if err := s.bus.PublishOutbound(s.outKey, out); err != nil {
				s.log.Debug("verdict: outbound publish failed", "err", err)
			}
		}
		if len(fx.verdicts) == 0 {
			done(nil)
			return
		}
		em.enqueue(wctx, fx.verdicts, done)
	}, stream.ConsumeOptions{StartAfter: last})
	return err
}

// rebuild replays the last RebuildWindow of the partition up to the durable consumer's start in
// rebuild mode (scores and sessions only). It returns the last replayed sequence.
func (s *Service) rebuild(ctx context.Context, p *partition, e *stream.Epoch) (last uint64, n int, err error) {
	spec := stream.ReplaySpec{Stream: stream.StreamSignals, Subject: stream.SignalsSubject(p.num), Since: s.now().Add(-s.opts.rebuild)}
	var cutoff time.Time
	if e.StartSeq > 0 {
		spec.UpTo = e.StartSeq - 1
	} else {
		// First epoch: the durable consumer starts by time; replay what is older than that.
		st, err := s.bus.JS.Stream(ctx, stream.StreamSignals)
		if err != nil {
			return 0, 0, err
		}
		info, err := st.Info(ctx)
		if err != nil {
			return 0, 0, err
		}
		spec.UpTo, cutoff = info.State.LastSeq, e.StartTime
	}
	n, err = s.bus.Replay(ctx, spec, func(m *stream.Msg) error {
		if !cutoff.IsZero() && !m.Time.Before(cutoff) {
			return errStopReplay
		}
		p.apply(m, false)
		last = m.Seq
		return nil
	})
	if errors.Is(err, errStopReplay) {
		err = nil
	}
	return last, n, err
}

// flushProbes writes aggregated probe timings every few seconds and once more at shutdown.
func (s *Service) flushProbes(ctx context.Context) {
	t := time.NewTicker(s.opts.probes)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
			_ = s.env.probes.flush(fctx, s.st)
			cancel()
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, storeTimeout)
			if err := s.env.probes.flush(fctx, s.st); err != nil {
				s.log.Debug("verdict: probe stats not written", "err", err)
			}
			cancel()
		}
	}
}
