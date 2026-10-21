package verdict

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

// Policy constants (ARCHITECTURE 5.6).
const (
	ReconsumeAge   = 2 * time.Minute  // signals received longer ago than this produce ALERT at most
	StaleEventAge  = 10 * time.Minute // signals whose normalised event time is older produce ALERT at most
	WatchInterval  = 2 * time.Minute  // at most one WatchRequest per player per interval
	WatchSeconds   = 60
	OverLimitGrace = 72 * time.Hour // free tier over its limit this long: verdicts capped at ALERT

	kickConfidence = 0.8
	banConfidence  = 0.9
	familyMinimum  = 5.0 // a family "contributes" with >= 5 points
	alertGrowth    = 1.5 // re-alert each time S grows 50 %

	seenIDs      = 1 << 17 // applied signal msg ids remembered per partition
	storeTimeout = 2 * time.Second
)

// Drop reasons (pandaeye_dropped_total).
const (
	dropDecode    = "verdict_decode"
	dropInvalid   = "verdict_invalid"
	dropDuplicate = "verdict_duplicate"
	dropErased    = "erased"
	dropDisabled  = "verdict_check_disabled"
)

// env is what partitions need from the service; tests build it directly.
type env struct {
	now       func() time.Time
	tenant    func(tenantID string) stream.TenantSettings
	erasedAt  func(tenantID string, player identity.UUID) (time.Time, bool)
	store     store.Store
	signals   telemetry.SignalSink
	probes    *probeStats
	outKey    []byte
	publicURL string
	metrics   *metrics.Metrics
	log       *slog.Logger
}

func (e *env) drop(reason string) {
	if e.metrics != nil {
		e.metrics.Drops.WithLabelValues(reason).Inc()
	}
}

// defaultSettings applies to tenants without a PE_TENANTS entry: documented defaults, enforcement
// off (every verdict is shadow).
func defaultSettings(tenantID string) stream.TenantSettings {
	return stream.TenantSettings{TenantID: tenantID, Leniency: DefaultLeniency, Mitigations: true,
		DiscordMinAction: pb.Action_ACTION_MITIGATE, Plan: store.PlanFree}
}

// effects is what applying one stream message produced.
type effects struct {
	verdicts []*pendingVerdict // publish in order: VerdictRecord (PubAck), then SignedOutbound, then Discord
	outbound []*pb.Outbound    // immediate core publishes: probe results, watch requests
}

// route is where a verdict goes.
type route struct {
	server        string // target server ("" until a network lookup resolves it)
	serverName    string
	network       string
	lookupNetwork bool   // BAN without an open session: any connected server of the network
	fallback      string // the newest signal's server (pending until it reconnects)
	fallbackName  string
}

// pendingVerdict is a verdict decided by a partition, published by its emitter.
type pendingVerdict struct {
	rec          *pb.VerdictRecord
	route        route
	settings     stream.TenantSettings
	cloudCheck   bool // at least one contributing check is cloud-computed (reputation)
	sourceRecvUs uint64
}

// trigger is the message an evaluation runs for.
type trigger struct {
	seq    uint64
	t      int64  // normalised event time
	recvUs uint64 // gateway receive time (cloud clock)
	origin *pb.Origin
	live   bool
}

// stale reports whether the trigger may only produce ALERTs: received more than ReconsumeAge ago
// (re-consumption) or with a normalised event time older than StaleEventAge (catch-up).
func (tr trigger) stale(now time.Time) bool {
	n := now.UnixMicro()
	return tr.recvUs == 0 || n-int64(tr.recvUs) > ReconsumeAge.Microseconds() || n-tr.t > StaleEventAge.Microseconds()
}

// normalise returns the normalised time of a plugin-clock value (t = time_us + clock_offset_us);
// without one, the cloud receive time.
func normalise(pluginUs uint64, offsetUs int64, recvUs uint64) int64 {
	if pluginUs == 0 {
		return int64(recvUs)
	}
	return int64(pluginUs) + offsetUs
}

func overLimit(ts stream.TenantSettings, now time.Time) bool {
	return ts.OverLimitSince > 0 && now.Sub(time.UnixMilli(ts.OverLimitSince)) >= OverLimitGrace
}

// forbidden reports whether a tenant flag forbids the action (the verdict is then shadow).
func forbidden(a pb.Action, ts stream.TenantSettings) bool {
	switch a {
	case pb.Action_ACTION_MITIGATE:
		return !ts.Mitigations
	case pb.Action_ACTION_KICK:
		return !ts.Autokick
	case pb.Action_ACTION_BAN:
		return !ts.Autoban
	}
	return false
}

// partition is the verdict state of one pe.signals.<NN> partition. apply runs on the consume loop
// goroutine; the mutex also covers housekeeping.
type partition struct {
	env *env
	num int

	mu      sync.Mutex
	players map[playerKey]*player
	seen    *idSet
}

func newPartition(e *env, num int) *partition {
	return &partition{env: e, num: num, players: map[playerKey]*player{}, seen: newIDSet(seenIDs)}
}

// apply processes one message of pe.signals.<NN>. live = false is rebuild mode: scores and sessions
// only, no actions, no outbound, no telemetry.
func (p *partition) apply(m *stream.Msg, live bool) effects {
	var envl pb.SignalEnvelope
	if err := proto.Unmarshal(m.Data, &envl); err != nil {
		p.env.drop(dropDecode)
		return effects{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id := m.Header.Get(nats.MsgIdHdr); id != "" && !p.seen.add(id) {
		p.env.drop(dropDuplicate)
		return effects{}
	}
	switch k := envl.Kind.(type) {
	case *pb.SignalEnvelope_Signal:
		if k.Signal != nil {
			return p.applySignal(k.Signal, m.Seq, live)
		}
	case *pb.SignalEnvelope_Session:
		if k.Session != nil {
			return p.applyMarker(k.Session, m.Seq, live)
		}
	}
	p.env.drop(dropInvalid)
	return effects{}
}

// playerFor returns the state of a player, dropping state that predates an erasure.
func (p *partition) playerFor(tenantID string, id identity.UUID, now time.Time) *player {
	k := playerKey{tenantID, id}
	pl := p.players[k]
	if pl != nil {
		if at, ok := p.env.erasedAt(tenantID, id); ok && !at.Before(pl.createdAt) {
			pl = nil
		}
	}
	if pl == nil {
		pl = newPlayer(k, now)
		p.players[k] = pl
	}
	return pl
}

// erased reports whether data received at recvUs predates the player's erasure (late data).
func (p *partition) erased(tenantID string, id identity.UUID, recvUs uint64) bool {
	at, ok := p.env.erasedAt(tenantID, id)
	return ok && (recvUs == 0 || int64(recvUs) <= at.UnixMicro())
}

func (p *partition) applySignal(sig *pb.Signal, seq uint64, live bool) (fx effects) {
	now := p.env.now()
	o := sig.GetOrigin()
	if o == nil || !identity.ValidTenantID(o.TenantId) {
		p.env.drop(dropInvalid)
		return
	}
	if sig.CheckId == checks.ProbeCheckID {
		if live {
			if out := p.probe(sig, now); out != nil {
				fx.outbound = append(fx.outbound, out)
			}
		}
		return
	}
	pid, err := identity.UUIDFromBytes(sig.Player)
	if err != nil || sig.CheckId == "" {
		p.env.drop(dropInvalid)
		return
	}
	if p.erased(o.TenantId, pid, sig.GatewayRecvUnixUs) {
		p.env.drop(dropErased)
		return
	}
	ts := p.env.tenant(o.TenantId)
	spec := specFor(sig)
	if !ts.CheckEnabled(sig.CheckId, spec.EnabledDefault) {
		p.env.drop(dropDisabled)
		return
	}
	t := normalise(sig.EventTimeUs, o.ClockOffsetUs, sig.GatewayRecvUnixUs)
	pl := p.playerFor(o.TenantId, pid, now)
	pl.touch(sig.PlayerName, o)
	sess := pl.sessionForSignal(sig.SessionId, o, sig.HotLoaded, t)
	sess.shadowMode = o.ShadowMode

	points := max(0, finite(sig.Points))
	// Hot-loaded sessions count half until the session is seeded (marker) and for 60 s after.
	if (sig.HotLoaded || sess.hotLoaded) && (!sess.marked || t < sess.startT+hotHalfWindow) {
		points /= 2
	}
	conf := clamp01(finite(sig.Confidence))
	if conf == 0 {
		conf = spec.Confidence
	}
	clip := sig.ClipId
	pl.score.advance(t)
	pl.score.add(spec, points, conf, clip, t)
	if points > 0 {
		pl.remember(sigRecord{check: sig.CheckId, category: spec.Category, points: points, conf: conf, info: sig.Info,
			data: sig.Data, t: t, clip: clip, local: sig.Local})
	}
	if live {
		p.env.signals.RecordSignal(sig)
	}
	return p.evaluate(pl, sess, trigger{seq: seq, t: t, recvUs: sig.GatewayRecvUnixUs, origin: o, live: live}, ts, now)
}

func (p *partition) applyMarker(m *pb.SessionMarker, seq uint64, live bool) (fx effects) {
	now := p.env.now()
	o := m.GetOrigin()
	pid, err := identity.UUIDFromBytes(m.Player)
	if o == nil || !identity.ValidTenantID(o.TenantId) || err != nil || m.SessionId == "" {
		p.env.drop(dropInvalid)
		return
	}
	if p.erased(o.TenantId, pid, m.GatewayRecvUnixUs) {
		p.env.drop(dropErased)
		return
	}
	pl := p.playerFor(o.TenantId, pid, now)
	pl.touch(m.PlayerName, o)
	at := int64(m.GatewayRecvUnixUs)
	startT := normalise(m.SessionStartUs, o.ClockOffsetUs, m.GatewayRecvUnixUs)
	tr := trigger{seq: seq, t: startT, recvUs: m.GatewayRecvUnixUs, origin: o, live: live}
	fresh := live && !tr.stale(now)
	switch m.Kind {
	case pb.SessionMarker_KIND_JOIN:
		s := pl.join(m, startT, fresh)
		pl.score.advance(startT) // in every mode, so a rebuild decays exactly like the live run
		// Mitigations are applied by backends; a proxy session never gets one.
		if !fresh || s.exempt || s.taken(pb.Action_ACTION_MITIGATE) || o.Platform == pb.Platform_PLATFORM_VELOCITY {
			return
		}
		// A rejoin with a still-high score re-issues MITIGATE for the new session.
		ts := p.env.tenant(o.TenantId)
		if overLimit(ts, now) {
			return
		}
		th := ThresholdsFor(ts.Leniency)
		sc := pl.score.snapshot()
		if sc.S >= th.Mitigate {
			s.actions |= actionBit(pb.Action_ACTION_MITIGATE)
			fx.verdicts = append(fx.verdicts, p.buildVerdict(pl, s, pb.Action_ACTION_MITIGATE, sc, th, ts, tr, now))
		}
	case pb.SessionMarker_KIND_UPDATE:
		pl.update(m, startT, max(at, startT))
	case pb.SessionMarker_KIND_QUIT:
		pl.quit(m, max(at, startT))
	}
	return
}

// evaluate runs the action ladder after a signal (ARCHITECTURE 5.6). In rebuild mode it only
// advances the ALERT ladder silently.
func (p *partition) evaluate(pl *player, sess *session, tr trigger, ts stream.TenantSettings, now time.Time) (fx effects) {
	th := ThresholdsFor(ts.Leniency)
	sc := pl.score.snapshot()
	var actions []pb.Action
	if sc.S >= th.Alert {
		if (pl.alertBase == 0 || sc.S >= alertGrowth*pl.alertBase) && (pl.lastAlertT == 0 || tr.t-pl.lastAlertT >= alertInterval) {
			pl.alertBase, pl.lastAlertT = sc.S, tr.t
			actions = append(actions, pb.Action_ACTION_ALERT)
		}
	} else {
		pl.alertBase = 0
	}
	if !tr.live {
		return
	}
	stale := tr.stale(now)
	// Stale or re-consumed signals, exempt/bedrock sessions, signals without a session and tenants
	// over their free-tier limit produce ALERT at most.
	if !stale && sess.id != "" && !sess.exempt && !overLimit(ts, now) {
		actions = append(actions, p.ladder(pl, sess, sc, th, tr)...)
	}
	for _, a := range actions {
		fx.verdicts = append(fx.verdicts, p.buildVerdict(pl, sess, a, sc, th, ts, tr, now))
	}
	if !stale && sess.open && sc.S >= th.Alert/2 && now.Sub(pl.lastWatch) >= WatchInterval {
		pl.lastWatch = now
		fx.outbound = append(fx.outbound, &pb.Outbound{TenantId: pl.key.tenant, ServerId: sess.server,
			Kind:                    &pb.Outbound_Watch{Watch: &pb.WatchRequest{Player: pl.key.player.Bytes(), Seconds: WatchSeconds}},
			SourceGatewayRecvUnixUs: tr.recvUs})
	}
	return
}

// ladder returns the MITIGATE/KICK/BAN actions the score earns that the session has not had yet,
// and marks them taken.
func (p *partition) ladder(pl *player, sess *session, sc Score, th Thresholds, tr trigger) []pb.Action {
	candidates := func() []pb.Action {
		var out []pb.Action
		if sc.S >= th.Mitigate && sess.open && !sess.taken(pb.Action_ACTION_MITIGATE) {
			out = append(out, pb.Action_ACTION_MITIGATE)
		}
		if sc.S >= th.Kick && sc.C >= kickConfidence && sess.open && sc.FamiliesOver5 >= 2 && !sess.taken(pb.Action_ACTION_KICK) {
			out = append(out, pb.Action_ACTION_KICK)
		}
		ladder := sc.S >= th.Ban && sc.FamiliesOver5 >= 2
		certain := sc.CertainAcc >= th.Ban/2
		if sc.C >= banConfidence && (ladder || certain) && hasClip(sc) && !sess.taken(pb.Action_ACTION_BAN) {
			out = append(out, pb.Action_ACTION_BAN)
		}
		return out
	}
	cands := candidates()
	if len(cands) == 0 {
		return nil
	}
	if !sess.actionsKnown {
		if err := p.loadActions(pl, sess); err != nil {
			p.env.log.Warn("verdict: cannot read the session's verdicts; holding back actions", "err", err)
			return nil
		}
		if cands = candidates(); len(cands) == 0 {
			return nil
		}
	}
	if slices.Contains(cands, pb.Action_ACTION_KICK) || slices.Contains(cands, pb.Action_ACTION_BAN) {
		covered, err := p.covered(pl, sc, tr)
		if err != nil {
			p.env.log.Warn("verdict: legit-cover check failed; holding back KICK/BAN", "err", err)
		}
		if covered || err != nil {
			cands = slices.DeleteFunc(cands, func(a pb.Action) bool {
				return a == pb.Action_ACTION_KICK || a == pb.Action_ACTION_BAN
			})
		}
	}
	for _, a := range cands {
		sess.actions |= actionBit(a)
	}
	return cands
}

// hasClip reports whether a committed clip is among the contributing signals.
func hasClip(sc Score) bool {
	for _, c := range sc.Contributing() {
		if len(c.Clips) > 0 {
			return true
		}
	}
	return false
}

// loadActions derives the actions already taken in a session from the stored verdicts (sessions
// first seen during a rebuild, inferred from a signal, or re-consumed).
func (p *partition) loadActions(pl *player, sess *session) error {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	vs, err := p.env.store.ListVerdictsBySession(ctx, pl.key.tenant, sess.id)
	if err != nil {
		return err
	}
	uuid := pl.key.player.String()
	for _, v := range vs {
		if v.PlayerUUID != uuid {
			continue
		}
		switch v.Action {
		case pb.Action_ACTION_MITIGATE, pb.Action_ACTION_KICK, pb.Action_ACTION_BAN:
			sess.actions |= actionBit(v.Action)
		}
	}
	sess.actionsKnown = true
	return nil
}

// covered reports whether a legit label or an overturned verdict of the player covers the signal
// window (from the first signal of the contributing checks to the trigger).
func (p *partition) covered(pl *player, sc Score, tr trigger) (bool, error) {
	from := tr.t
	for _, c := range sc.Contributing() {
		if c.FirstT > 0 && c.FirstT < from {
			from = c.FirstT
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	return p.env.store.HasLegitCover(ctx, pl.key.tenant, pl.key.player.String(), time.UnixMicro(from).UTC(), time.UnixMicro(tr.t).UTC())
}

// buildVerdict creates a verdict with its deterministic id, evidence and route.
func (p *partition) buildVerdict(pl *player, sess *session, a pb.Action, sc Score, th Thresholds, ts stream.TenantSettings,
	tr trigger, now time.Time) *pendingVerdict {
	tenant := pl.key.tenant
	id := identity.VerdictID(tenant, pl.key.player, sess.id, int32(a), tr.seq)
	contributing := sc.Contributing()
	ids := make([]string, len(contributing))
	cloud := false
	for i, c := range contributing {
		ids[i] = c.Check
		cloud = cloud || c.Cloud || checks.Family(c.Check) == "ml"
	}
	v := &pb.Verdict{
		VerdictId: id, Player: pl.key.player.Bytes(), PlayerName: pl.name, Action: a, Reason: reason(sc), Category: sc.Dominant,
		Score: round3(sc.S), Confidence: round3(sc.C), Checks: ids, CreatedUnixMs: uint64(now.UnixMilli()),
		Shadow: !ts.Enforcement || tr.origin.GetShadowMode() || forbidden(a, ts),
	}
	switch a {
	case pb.Action_ACTION_MITIGATE:
		v.Mitigations, v.Strength = mitigationsFor(sc.Dominant), mitigationStrength(sc.S, th)
	case pb.Action_ACTION_BAN:
		v.AppealCode = identity.AppealCode(p.env.outKey, id)
	}
	if p.env.publicURL != "" {
		v.EvidenceUrl = strings.TrimRight(p.env.publicURL, "/") + "/verdicts/" + id
	}
	r := p.route(pl, sess, a, tr)
	rec := &pb.VerdictRecord{TenantId: tenant, ServerId: r.server, NetworkId: r.network, Verdict: v,
		EvidenceJson: buildEvidence(pl, sess, sc, th, ts.Leniency).marshal(), SessionId: sess.id,
		CreatedUnixUs: uint64(now.UnixMicro()), ClipIds: clipIDs(contributing)}
	return &pendingVerdict{rec: rec, route: r, settings: ts, cloudCheck: cloud, sourceRecvUs: tr.recvUs}
}

// route picks the target server (ARCHITECTURE 5.6): the open session's server; for BAN without an
// open session any connected server of the network (resolved by the emitter), else the server of
// the newest signal.
func (p *partition) route(pl *player, sess *session, a pb.Action, tr trigger) route {
	r := route{network: firstNonEmpty(sess.network, pl.newestNetwork, tr.origin.GetNetworkId())}
	if sess.open && sess.server != "" {
		r.server, r.serverName = sess.server, sess.serverName
		return r
	}
	for _, s := range pl.openSessions() {
		if s.server != "" {
			r.server, r.serverName = s.server, s.serverName
			return r
		}
	}
	r.fallback, r.fallbackName = firstNonEmpty(pl.newestServer, tr.origin.GetServerId()), pl.newestServerName
	if r.fallbackName == "" {
		r.fallbackName = tr.origin.GetServerName()
	}
	if a == pb.Action_ACTION_BAN && r.network != "" {
		r.lookupNetwork = true
	}
	r.server, r.serverName = r.fallback, r.fallbackName
	return r
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// probe answers a probe signal with Outbound{probe_result} and records its cloud-side timings.
func (p *partition) probe(sig *pb.Signal, now time.Time) *pb.Outbound {
	o := sig.Origin
	recv := sig.GatewayRecvUnixUs
	nowUs := now.UnixMicro()
	if !identity.ValidServerID(o.ServerId) || recv == 0 || nowUs-int64(recv) > ReconsumeAge.Microseconds() {
		return nil // a re-consumed probe is no longer awaited by the plugin
	}
	if p.env.metrics != nil {
		if sig.WorkerUnixUs >= recv {
			p.env.metrics.ProbeRTT.WithLabelValues(metrics.StageGatewayToWorker).Observe(float64(sig.WorkerUnixUs-recv) / 1e6)
		}
		if sig.WorkerUnixUs > 0 && nowUs >= int64(sig.WorkerUnixUs) {
			p.env.metrics.ProbeRTT.WithLabelValues(metrics.StageWorkerToVerdict).Observe(float64(nowUs-int64(sig.WorkerUnixUs)) / 1e6)
		}
	}
	if p.env.probes != nil {
		p.env.probes.observe(o.TenantId, o.ServerId, now, nowUs-int64(recv))
	}
	return &pb.Outbound{TenantId: o.TenantId, ServerId: o.ServerId, SourceGatewayRecvUnixUs: recv,
		Kind: &pb.Outbound_ProbeResult{ProbeResult: &pb.ProbeResult{ProbeId: sig.ProbeId, Player: sig.Player,
			EventTimeUs: sig.EventTimeUs, GatewayRecvUnixUs: recv, WorkerUnixUs: sig.WorkerUnixUs, VerdictUnixUs: uint64(nowUs)}}}
}

// housekeep evicts idle players (every accumulator < 0.5 and no session open for an hour) and the
// state of erased players.
func (p *partition) housekeep() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.env.now()
	n := 0
	for k, pl := range p.players {
		erased := false
		if at, ok := p.env.erasedAt(k.tenant, k.player); ok && !at.Before(pl.createdAt) {
			erased = true
		}
		if erased || pl.housekeep(now.UnixMicro()) {
			delete(p.players, k)
			n++
		}
	}
	return n
}

// errStopReplay ends a rebuild replay at the durable consumer's start time.
var errStopReplay = errors.New("verdict: replay reached the durable consumer")
