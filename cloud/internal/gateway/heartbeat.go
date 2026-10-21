package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// silentIntervals: either side closes after this many heartbeat intervals without a frame.
const silentIntervals = 3

// handlePing answers with a Pong carrying the current RemoteConfig, keeps online_players for the
// PE_CONNECTIONS value and records the plugin-measured probe RTT.
func (s *session) handlePing(p *pb.Ping, at time.Time) bool {
	s.online.Store(p.OnlinePlayers)
	pong := &pb.Pong{Nonce: p.Nonce, ClientTimeUs: p.ClientTimeUs, ServerTimeUs: uint64(time.Now().UnixMicro()), Config: s.remoteConfig()}
	if err := s.write(&pb.ServerFrame{Kind: &pb.ServerFrame_Pong{Pong: pong}}); err != nil {
		s.terminate("write_failed", nil)
		return false
	}
	if p.LastProbeRttUs > 0 {
		s.probes.observe(s, int64(p.LastProbeRttUs), at)
		s.g.m.ProbeRTT.WithLabelValues(metrics.StagePlugin).Observe(float64(p.LastProbeRttUs) / 1e6)
	}
	return true
}

// tickLoop renews the connection entry, schedules the verdict sweep, closes silent connections and
// keeps the plugin's own silence timer satisfied while this side is not reading (backpressure or a
// stuck publish).
func (s *session) tickLoop() {
	o := s.g.opts
	renew := time.NewTicker(o.RenewInterval)
	defer renew.Stop()
	sweep := time.NewTicker(o.SweepInterval)
	defer sweep.Stop()
	beat := time.NewTicker(max(o.HeartbeatInterval/5, 10*time.Millisecond))
	defer beat.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-renew.C:
			s.renew()
		case now := <-sweep.C:
			s.runAux("sweep", func(ctx context.Context) { s.pending(ctx, false) })
			s.pruneDelivered(now)
			if b := s.probes.take(now, false); b != nil {
				s.runAux("probe_stats", func(ctx context.Context) { s.recordProbes(ctx, b) })
			}
		case now := <-beat.C:
			s.heartbeat(now)
		}
	}
}

func (s *session) heartbeat(now time.Time) {
	hb := s.g.opts.HeartbeatInterval
	if since := s.readSince.Load(); since != 0 {
		idle := now.Sub(time.Unix(0, max(since, s.lastRecv.Load())))
		if idle > silentIntervals*hb {
			s.log.Info("closing a silent connection", "silent_for", idle.Round(time.Millisecond).String())
			s.terminate("silent", nil)
			return
		}
	}
	if now.Sub(time.Unix(0, s.lastSend.Load())) >= hb {
		pong := &pb.Pong{ServerTimeUs: uint64(now.UnixMicro()), Config: s.remoteConfig()}
		if err := s.write(&pb.ServerFrame{Kind: &pb.ServerFrame_Pong{Pong: pong}}); err != nil {
			s.terminate("write_failed", nil)
		}
	}
}

// probeRecorder batches the plugin-reported probe RTTs of one connection into the minute buckets
// of probe_stats (source plugin): one store write per server per minute.
type probeRecorder struct {
	mu     sync.Mutex
	minute time.Time
	h      store.ProbeHistogram
	maxUs  int64
}

type probeBucket struct {
	minute time.Time
	h      store.ProbeHistogram
	maxUs  int64
}

func (p *probeRecorder) observe(s *session, us int64, at time.Time) {
	m := at.UTC().Truncate(time.Minute)
	p.mu.Lock()
	var done *probeBucket
	if !m.Equal(p.minute) {
		done = p.takeLocked()
		p.minute = m
	}
	p.h.Observe(us)
	p.maxUs = max(p.maxUs, us)
	p.mu.Unlock()
	if done != nil {
		s.runAux("probe_stats", func(ctx context.Context) { s.recordProbes(ctx, done) })
	}
}

// take returns the pending bucket when its minute is over (or always with force) and resets it.
func (p *probeRecorder) take(now time.Time, force bool) *probeBucket {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !force && now.UTC().Truncate(time.Minute).Equal(p.minute) {
		return nil
	}
	return p.takeLocked()
}

func (p *probeRecorder) takeLocked() *probeBucket {
	if p.h.Count() == 0 {
		return nil
	}
	b := &probeBucket{minute: p.minute, h: p.h, maxUs: p.maxUs}
	p.h, p.maxUs = store.ProbeHistogram{}, 0
	return b
}

func (p *probeRecorder) flush(ctx context.Context, s *session) {
	if b := p.take(time.Now(), true); b != nil {
		s.recordProbes(ctx, b)
	}
}

func (s *session) recordProbes(ctx context.Context, b *probeBucket) {
	if err := s.g.d.Store.RecordProbeStats(ctx, s.tenantID, s.serverID, store.ProbeSourcePlugin, b.minute, b.h, b.maxUs); err != nil {
		s.log.Warn("recording probe statistics failed", "err", err)
	}
}
