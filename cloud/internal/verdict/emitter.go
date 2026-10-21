package verdict

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

const emitQueueSize = 1024

type emitJob struct {
	ctx      context.Context // the consume handler's context (ends when the lease is lost)
	verdicts []*pendingVerdict
	done     func(error)
}

// emitter publishes one partition's verdicts in decision order on its own goroutine: VerdictRecord
// to pe.verdicts.<tenant> (waiting for the PubAck), then the SignedOutbound to
// pe.out.<tenant>.<server>, then Discord. The consume loop acks a message only after done.
type emitter struct {
	s    *Service
	ch   chan emitJob
	stop chan struct{}
	wg   sync.WaitGroup
}

func newEmitter(s *Service) *emitter {
	e := &emitter{s: s, ch: make(chan emitJob, emitQueueSize), stop: make(chan struct{})}
	e.wg.Add(1)
	go e.run()
	return e
}

// enqueue hands a message's verdicts to the emitter; it blocks while the queue is full
// (backpressure on the consume loop) and gives up when ctx ends.
func (e *emitter) enqueue(ctx context.Context, vs []*pendingVerdict, done func(error)) {
	select {
	case e.ch <- emitJob{ctx: ctx, verdicts: vs, done: done}:
	case <-ctx.Done():
		done(context.Cause(ctx))
	case <-e.stop:
		done(errors.New("verdict: emitter stopped"))
	}
}

// close stops the emitter; queued jobs are not completed (their messages are redelivered).
func (e *emitter) close() {
	close(e.stop)
	e.wg.Wait()
}

func (e *emitter) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.stop:
			return
		case j := <-e.ch:
			var err error
			for _, pv := range j.verdicts {
				if err = e.s.emit(j.ctx, pv); err != nil {
					break
				}
			}
			j.done(err)
		}
	}
}

// emit resolves the route, publishes the VerdictRecord (PubAck), the SignedOutbound and the
// Discord alert of one verdict.
func (s *Service) emit(ctx context.Context, pv *pendingVerdict) error {
	rec := pv.rec
	v := rec.Verdict
	if pv.route.lookupNetwork {
		s.resolveNetwork(ctx, pv)
	}
	if v.Action == pb.Action_ACTION_BAN && !v.Shadow && pv.cloudCheck && pv.settings.ReputationOptIn {
		rec.ReputationEligible = s.trust.trusted(ctx, rec.TenantId, s.now())
	}
	data, err := proto.Marshal(rec)
	if err != nil {
		return err
	}
	subj, err := stream.VerdictsSubject(rec.TenantId)
	if err != nil {
		s.log.Warn("verdict: invalid tenant id; verdict dropped", "verdict_id", v.VerdictId)
		return nil
	}
	if _, err := s.bus.Publish(ctx, stream.Message{Subject: subj, Data: data, MsgID: stream.VerdictRecordMsgID(v.VerdictId)}); err != nil {
		if errors.Is(err, stream.ErrPermanent) {
			s.log.Error("verdict: record rejected by the stream; verdict dropped", "verdict_id", v.VerdictId, "err", err)
			return nil
		}
		return err
	}
	if rec.ServerId != "" {
		out := &pb.Outbound{TenantId: rec.TenantId, ServerId: rec.ServerId, Kind: &pb.Outbound_Verdict{Verdict: v},
			SourceGatewayRecvUnixUs: pv.sourceRecvUs}
		if err := s.bus.PublishOutbound(s.outKey, out); err != nil {
			// The verdict is stored: the gateway delivers it from the store on its next sweep.
			s.log.Warn("verdict: outbound publish failed; delivery falls back to the gateway sweep", "verdict_id", v.VerdictId, "err", err)
		}
	}
	s.m.Verdicts.WithLabelValues(ActionName(v.Action), strconv.FormatBool(v.Shadow)).Inc()
	s.discord.enqueue(pv)
	return nil
}

// resolveNetwork routes a BAN of an offline player to a connected server of the same network, else
// leaves the fallback (the newest signal's server, pending until it reconnects).
func (s *Service) resolveNetwork(ctx context.Context, pv *pendingVerdict) {
	lctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	conns, err := s.bus.ListConnections(lctx, pv.rec.TenantId)
	if err != nil {
		s.log.Debug("verdict: connection lookup failed; using the newest signal's server", "err", err)
		return
	}
	var match []stream.Connection
	for _, c := range conns {
		if c.Info.NetworkID == pv.route.network {
			match = append(match, c)
		}
	}
	if len(match) == 0 {
		return
	}
	// Prefer the fallback server when it is connected, then backends over proxies, then by id.
	slices.SortFunc(match, func(a, b stream.Connection) int {
		af, bf := a.ServerID == pv.route.fallback, b.ServerID == pv.route.fallback
		if af != bf {
			if af {
				return -1
			}
			return 1
		}
		ap, bp := a.Info.Platform == pb.Platform_PLATFORM_VELOCITY, b.Info.Platform == pb.Platform_PLATFORM_VELOCITY
		if ap != bp {
			if bp {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ServerID, b.ServerID)
	})
	c := match[0]
	pv.rec.ServerId, pv.route.server = c.ServerID, c.ServerID
	if c.ServerID != pv.route.fallback {
		pv.route.serverName = ""
	}
	if c.Info.ShadowMode {
		pv.rec.Verdict.Shadow = true
	}
}

// trustCache caches whether tenants are trusted for the reputation network (5.11).
type trustCache struct {
	s  *Service
	mu sync.Mutex
	m  map[string]trustEntry
}

type trustEntry struct {
	trusted bool
	at      time.Time
}

const trustTTL = 10 * time.Minute

func (c *trustCache) trusted(ctx context.Context, tenantID string, now time.Time) bool {
	c.mu.Lock()
	e, ok := c.m[tenantID]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < trustTTL {
		return e.trusted
	}
	tctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	ti, err := c.s.st.TrustInputs(tctx, tenantID)
	if err != nil {
		return false
	}
	e = trustEntry{trusted: TrustedTenant(ti, now), at: now}
	c.mu.Lock()
	c.m[tenantID] = e
	c.mu.Unlock()
	return e.trusted
}
