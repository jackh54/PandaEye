package worker

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// Session rules (ARCHITECTURE 5.5).
const (
	bufferMaxAgeUs      = 5_000_000 // unknown-session events wait at most 5 s for session info
	bufferPerSession    = 256
	bufferPerPartition  = 20_000
	quitKeep            = 10 * time.Minute
	silentEvict         = 15 * time.Minute
	idleForEviction     = time.Minute // memory pressure: sessions silent this long go first
	trackedSessionBytes = 96 << 10    // accounted state of a tracked session besides its clip ring
	plainSessionBytes   = 1 << 10     // untracked or proxy session
)

type serverKey struct{ tenant, server string }

type playerKey struct {
	tenant string
	player identity.UUID
}

type sessKey struct {
	tenant string
	player identity.UUID
	server string
}

// erasureSource reports erasure tombstones (KV PE_ERASURES mirror).
type erasureSource interface {
	Erased(tenantID string, player identity.UUID) (time.Time, bool)
}

type noErasures struct{}

func (noErasures) Erased(string, identity.UUID) (time.Time, bool) { return time.Time{}, false }

// env is what every partition engine of a process shares.
type env struct {
	log       *slog.Logger
	m         *metrics.Metrics
	wm        *workerMetrics
	factories []checks.Factory
	tenants   tenantSource
	erasures  erasureSource
	budget    *budget
	clips     *clipPolicy
	saver     *clipSaver // nil: clips are not saved (replay)
	sessions  telemetry.SessionSink
	windows   telemetry.FeatureSink
	scorer    ml.Scorer
	features  bool // compute feature windows
	now       func() time.Time
	panics    atomic.Int64 // detector panics (logged at most once per minute)
	panicLog  atomic.Int64
}

func (e *env) plan(tenant string) planLimits {
	s, _ := e.tenants.Get(tenant)
	return limitsFor(s.Plan)
}

func (e *env) detectorPanic(name string, r any) {
	e.panics.Add(1)
	e.m.Drops.WithLabelValues("detector_panic").Inc()
	now := e.now().Unix()
	if last := e.panicLog.Load(); now-last >= 60 && e.panicLog.CompareAndSwap(last, now) {
		e.log.Error("check detector panicked", "factory", name, "panic", fmt.Sprint(r), "total", e.panics.Load())
	}
}

type player struct {
	key         playerKey
	sessions    map[string]*session // backend sessions by server id
	latest      *session            // most recently joined session: per-player accumulators follow it
	points      []timedPoints       // signal points of the last 60 s (clip trigger)
	lastClipUs  int64
	firstRecvUs uint64 // gateway receive time of the oldest data held (erasure)
}

type timedPoints struct {
	t      int64
	points float64
}

// addPoints records points at t and returns the player's points in the last 60 s.
func (p *player) addPoints(t int64, pts float64) float64 {
	if pts > 0 {
		p.points = append(p.points, timedPoints{t, pts})
	}
	keep := p.points[:0]
	sum := 0.0
	for _, x := range p.points {
		if x.t > t-clipPointsSpanUs {
			keep = append(keep, x)
			sum += x.points
		}
	}
	p.points = keep
	return sum
}

type detector struct {
	name string
	d    checks.Detector
}

type session struct {
	key            sessKey
	p              *player
	id             string
	name           string
	networkSession string
	startUs        uint64
	hotLoaded      bool
	exempt         bool
	bedrock        bool
	join           *pb.PlayerJoin
	origin         *pb.Origin // of the newest batch
	tracked        bool       // within the tenant's player cap: checks, clips and windows run

	view     *checks.View
	ctx      *checks.Ctx
	dets     []detector
	features *ml.Session
	ring     *clipRing
	joinRaw  []byte
	stateRaw []byte

	lastT    int64
	lastSeen time.Time
	closed   bool
	closedAt time.Time
	cost     int64
}

func (s *session) judged() bool {
	return s.tracked && s.view != nil && !s.exempt && !s.bedrock && !s.view.Exempt && !s.view.Bedrock
}

type proxySession struct {
	id, name, networkSession string
	startUs                  uint64
	hotLoaded, exempt        bool
	join                     *pb.PlayerJoin
	lastSeen                 time.Time
}

type bufItem struct {
	m   *batchMeta
	i   int
	ev  *pb.Event
	raw []byte
	t   int64
}

type buffer struct {
	items     []bufItem
	firstWall time.Time
}

// batchMeta is the context of one StreamBatch.
type batchMeta struct {
	origin   *pb.Origin
	tenant   string
	server   string
	batchSeq uint64
	recvUs   uint64
	replayed bool
	offset   int64
	proxy    bool
	silent   bool
}

// outMsg is one message for pe.signals.<NN>. A signal with clip waits for the clip's commit and then
// carries its id.
type outMsg struct {
	subject string
	msgID   string
	env     *pb.SignalEnvelope
	clip    *clipJob
}

type ordKey struct {
	m     *batchMeta
	i     int
	check string
}

// engine is the state of one partition: sessions, dedup and buffers. One goroutine at a time drives
// it (the partition mutex); handle returns the messages derived from a StreamBatch.
type engine struct {
	part     int
	env      *env
	dedup    *dedupTable
	lastSeq  uint64
	newestUs int64 // newest normalised event time of the partition

	players  map[playerKey]*player
	sessions map[sessKey]*session
	byServer map[serverKey]map[*session]struct{}
	proxies  map[sessKey]*proxySession
	buffers  map[sessKey]*buffer
	buffered int

	out      []outMsg
	emits    []checks.Emission
	ordinals map[ordKey]int
	deltas   map[string]int64
}

func newEngine(part int, e *env, dedup *dedupTable) *engine {
	if dedup == nil {
		dedup = newDedupTable()
	}
	return &engine{part: part, env: e, dedup: dedup, players: map[playerKey]*player{}, sessions: map[sessKey]*session{},
		byServer: map[serverKey]map[*session]struct{}{}, proxies: map[sessKey]*proxySession{}, buffers: map[sessKey]*buffer{},
		ordinals: map[ordKey]int{}, deltas: map[string]int64{}}
}

// handle processes the StreamBatch stored at stream sequence seq (0: no sequence, e.g. a clip
// replay). raws are the encoded events (splitEvents), nil when unavailable. In silent mode (warm
// start) state is updated and nothing is emitted.
func (e *engine) handle(seq uint64, sb *pb.StreamBatch, raws [][]byte, silent bool) []outMsg {
	if seq != 0 {
		if seq <= e.lastSeq {
			e.env.m.Drops.WithLabelValues("duplicate_message").Inc()
			return nil
		}
		e.lastSeq = seq
	}
	o := sb.GetOrigin()
	if o == nil || !identity.ValidTenantID(o.GetTenantId()) || !identity.ValidServerID(o.GetServerId()) {
		e.env.m.Drops.WithLabelValues("invalid_batch").Inc()
		return nil
	}
	if bs := sb.GetBatchSeq(); bs != 0 && !e.dedup.admit(serverKey{o.GetTenantId(), o.GetServerId()}, bs) {
		e.env.m.Drops.WithLabelValues("duplicate_batch").Inc()
		return nil
	}
	m := &batchMeta{origin: o, tenant: o.GetTenantId(), server: o.GetServerId(), batchSeq: sb.GetBatchSeq(),
		recvUs: sb.GetGatewayRecvUnixUs(), replayed: sb.GetReplayed(), offset: o.GetClockOffsetUs(),
		proxy: o.GetPlatform() == pb.Platform_PLATFORM_VELOCITY, silent: silent}
	if len(raws) != len(sb.GetEvents()) {
		raws = nil
	}
	for i, ev := range sb.GetEvents() {
		var raw []byte
		if raws != nil {
			raw = raws[i]
		}
		e.event(m, i, ev, raw)
	}
	return e.flush()
}

// flush returns the collected messages and reports state deltas to the budget.
func (e *engine) flush() []outMsg {
	for t, d := range e.deltas {
		e.env.budget.add(t, d)
		delete(e.deltas, t)
	}
	clear(e.ordinals)
	out := e.out
	e.out = nil
	return out
}

func (e *engine) account(s *session, delta int64) {
	if delta == 0 {
		return
	}
	s.cost += delta
	e.deltas[s.key.tenant] += delta
}

func (e *engine) event(m *batchMeta, i int, ev *pb.Event, raw []byte) {
	t := int64(ev.GetTimeUs()) + m.offset
	e.newestUs = max(e.newestUs, t)
	switch k := ev.GetKind().(type) {
	case *pb.Event_Probe:
		e.probe(m, i, ev, k.Probe)
		return
	case *pb.Event_Stall:
		e.stall(m, i, ev)
		return
	}
	if len(ev.GetPlayer()) != 16 {
		return // other server-level events carry nothing for the worker
	}
	uuid, _ := identity.UUIDFromBytes(ev.GetPlayer())
	if e.erased(m, uuid) {
		return
	}
	if m.proxy {
		e.proxyEvent(m, i, ev, uuid) // Velocity events never touch backend sessions
		return
	}
	key := sessKey{m.tenant, uuid, m.server}
	switch k := ev.GetKind().(type) {
	case *pb.Event_Join:
		e.join(m, i, ev, raw, key, k.Join)
	case *pb.Event_Quit:
		e.quit(m, i, ev, raw, key)
	default:
		s := e.sessions[key]
		switch {
		case s == nil:
			e.buffer(key, m, i, ev, raw)
		case s.closed:
			e.env.m.Drops.WithLabelValues("session_closed").Inc()
		default:
			e.apply(s, m, i, ev, raw)
		}
	}
}

// erased reports whether the event is late data of an erased player and drops the player's state
// held from before the erasure.
func (e *engine) erased(m *batchMeta, uuid identity.UUID) bool {
	at, ok := e.env.erasures.Erased(m.tenant, uuid)
	if !ok {
		return false
	}
	atUs := uint64(at.UnixMicro())
	if p := e.players[playerKey{m.tenant, uuid}]; p != nil && p.firstRecvUs <= atUs {
		e.dropPlayer(p)
	}
	if m.recvUs <= atUs {
		e.env.m.Drops.WithLabelValues("erased").Inc()
		return true
	}
	return false
}

func (e *engine) player(m *batchMeta, uuid identity.UUID) *player {
	k := playerKey{m.tenant, uuid}
	p := e.players[k]
	if p == nil {
		p = &player{key: k, sessions: map[string]*session{}, firstRecvUs: m.recvUs}
		e.players[k] = p
	}
	p.firstRecvUs = min(p.firstRecvUs, m.recvUs)
	return p
}

// ---------------------------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------------------------

func (e *engine) join(m *batchMeta, i int, ev *pb.Event, raw []byte, key sessKey, j *pb.PlayerJoin) {
	t := int64(ev.GetTimeUs()) + m.offset
	cur := e.sessions[key]
	if j.GetUpdate() && cur != nil && !cur.closed && (j.GetSession() == "" || j.GetSession() == cur.id) {
		if t < cur.lastT {
			e.env.m.Drops.WithLabelValues("stale").Inc()
			return
		}
		e.applyAt(cur, m, i, ev, raw, t)
		e.marker(m, i, cur, pb.SessionMarker_KIND_UPDATE)
		return
	}
	if cur != nil {
		e.removeSession(cur, m, i) // replaced: a new real join, or an update for a session we missed
	}
	full := j
	synth := ev
	if j.GetUpdate() {
		// A session we did not see start (worker restart, handover): recover it from the
		// re-announcement without resetting anything later.
		full = proto.CloneOf(j)
		full.Update = false
		synth = &pb.Event{Player: ev.GetPlayer(), TimeUs: ev.GetTimeUs(), Tick: ev.GetTick(), Kind: &pb.Event_Join{Join: full}}
	}
	s := e.newSession(m, key, full, ev)
	buf := e.takeBuffer(key)
	e.applyAt(s, m, i, synth, raw, t)
	if j.GetUpdate() {
		if len(buf) > 0 {
			s.lastT = buf[0].t // the buffered events precede the re-announcement
		}
		e.marker(m, i, s, pb.SessionMarker_KIND_UPDATE)
	} else {
		e.marker(m, i, s, pb.SessionMarker_KIND_JOIN)
	}
	for _, b := range buf {
		if !j.GetUpdate() && b.t < t {
			e.env.m.Drops.WithLabelValues("stale").Inc() // from before this session began
			continue
		}
		bm := *b.m
		bm.silent = m.silent // emitted (or not) in the current mode, with the original msg ids
		e.apply(s, &bm, b.i, b.ev, b.raw)
	}
	s.lastT = max(s.lastT, t)
}

func (e *engine) newSession(m *batchMeta, key sessKey, j *pb.PlayerJoin, ev *pb.Event) *session {
	p := e.player(m, key.player)
	s := &session{key: key, p: p, id: j.GetSession(), name: j.GetName(), networkSession: j.GetNetworkSession(),
		startUs: ev.GetTimeUs(), hotLoaded: j.GetHotLoaded(), exempt: j.GetExempt(), bedrock: j.GetBedrock(),
		join: proto.CloneOf(j), origin: m.origin, lastSeen: e.env.now()}
	s.join.Update = false
	s.tracked = !j.GetBedrock() && e.env.budget.track(key.tenant, e.env.plan(key.tenant).players)
	cost := int64(plainSessionBytes)
	if s.tracked {
		cost = trackedSessionBytes
		s.view = checks.NewView()
		s.ctx = checks.NewCtx(s.view, tenantConfig{src: e.env.tenants, tenant: key.tenant}, func(em checks.Emission) {
			e.emits = append(e.emits, em)
		})
		var gcd checks.GCDSource
		for _, f := range e.env.factories {
			d := f.New()
			if g, ok := d.(checks.GCDSource); ok && gcd == nil {
				gcd = g
			}
			s.dets = append(s.dets, detector{name: f.Name, d: d})
		}
		s.ring = &clipRing{}
		if e.env.features {
			s.features = ml.NewSession(ml.SessionInfo{TenantID: key.tenant, Player: key.player, ServerID: key.server,
				SessionID: s.id, Protocol: j.GetProtocolVersion(), HotLoaded: j.GetHotLoaded()}, gcd, e.env.now)
		}
	} else if !j.GetBedrock() {
		e.env.m.Drops.WithLabelValues("player_cap").Inc()
	}
	e.account(s, cost)
	// The newest session of the player takes over the per-player accumulators; the previous one's
	// feature windows end here (replacement by a newer session).
	if old := p.latest; old != nil && old != s && old.features != nil {
		e.windows(nil, -1, old, old.features.Close())
		old.features = nil
	}
	p.latest = s
	p.sessions[key.server] = s
	e.sessions[key] = s
	sk := serverKey{key.tenant, key.server}
	if e.byServer[sk] == nil {
		e.byServer[sk] = map[*session]struct{}{}
	}
	e.byServer[sk][s] = struct{}{}
	return s
}

// removeSession forgets a session (replacement, eviction, erasure): its windows are finalised, its
// slot and bytes released. No marker is published.
func (e *engine) removeSession(s *session, m *batchMeta, i int) {
	if s.features != nil {
		e.windows(m, i, s, s.features.Close())
		s.features = nil
	}
	e.release(s)
	delete(e.sessions, s.key)
	if set := e.byServer[serverKey{s.key.tenant, s.key.server}]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(e.byServer, serverKey{s.key.tenant, s.key.server})
		}
	}
	if s.p.sessions[s.key.server] == s {
		delete(s.p.sessions, s.key.server)
	}
	if s.p.latest == s {
		s.p.latest = nil
	}
	if len(s.p.sessions) == 0 {
		delete(e.players, s.p.key)
	}
}

// release frees a session's heavy state and its player slot (idempotent).
func (e *engine) release(s *session) {
	if s.tracked {
		e.env.budget.untrack(s.key.tenant)
		s.tracked = false
	}
	s.view, s.ctx, s.dets, s.ring, s.features, s.joinRaw, s.stateRaw = nil, nil, nil, nil, nil, nil, nil
	e.account(s, -s.cost)
}

func (e *engine) dropPlayer(p *player) {
	for _, s := range p.sessions {
		e.removeSession(s, nil, -1)
	}
	for k := range e.buffers {
		if k.tenant == p.key.tenant && k.player == p.key.player {
			e.buffered -= len(e.buffers[k].items)
			delete(e.buffers, k)
		}
	}
	delete(e.players, p.key)
	e.env.m.Drops.WithLabelValues("erased_state").Inc()
}

func (e *engine) quit(m *batchMeta, i int, ev *pb.Event, raw []byte, key sessKey) {
	s := e.sessions[key]
	if s == nil || s.closed {
		// A quit for a session this worker never saw: tell the verdict service the server's session
		// of this player is over (session id unknown).
		if bi := e.takeBuffer(key); len(bi) > 0 {
			e.env.m.Drops.WithLabelValues("unknown_session").Add(float64(len(bi)))
		}
		if s == nil {
			uuid := key.player
			e.publishMarker(m, i, &pb.SessionMarker{Origin: m.origin, Player: uuid.Bytes(), Kind: pb.SessionMarker_KIND_QUIT,
				GatewayRecvUnixUs: m.recvUs}, nil)
		}
		return
	}
	t := int64(ev.GetTimeUs()) + m.offset
	e.applyAt(s, m, i, ev, raw, max(t, s.lastT)) // a quit is never dropped as stale
	if s.features != nil {
		e.windows(m, i, s, s.features.Close())
		s.features = nil
	}
	e.marker(m, i, s, pb.SessionMarker_KIND_QUIT)
	s.closed, s.closedAt = true, e.env.now()
	e.release(s)
	e.account(s, plainSessionBytes)
}

func (e *engine) proxyEvent(m *batchMeta, i int, ev *pb.Event, uuid identity.UUID) {
	key := sessKey{m.tenant, uuid, m.server}
	ps := e.proxies[key]
	kind := pb.SessionMarker_KIND_UPDATE
	switch k := ev.GetKind().(type) {
	case *pb.Event_Join:
		j := k.Join
		if !j.GetUpdate() || ps == nil || (j.GetSession() != "" && j.GetSession() != ps.id) {
			if !j.GetUpdate() {
				kind = pb.SessionMarker_KIND_JOIN
			}
			ps = &proxySession{id: j.GetSession(), startUs: ev.GetTimeUs(), hotLoaded: j.GetHotLoaded()}
			e.proxies[key] = ps
		}
		if j.GetName() != "" {
			ps.name = j.GetName()
		}
		if j.GetNetworkSession() != "" {
			ps.networkSession = j.GetNetworkSession()
		}
		ps.exempt = j.GetExempt() || j.GetBedrock()
		ps.join = proto.CloneOf(j)
	case *pb.Event_Quit:
		kind = pb.SessionMarker_KIND_QUIT
		delete(e.proxies, key)
		if ps == nil {
			ps = &proxySession{}
		}
	default:
		if ps != nil {
			ps.lastSeen = e.env.now()
		}
		return
	}
	ps.lastSeen = e.env.now()
	e.publishMarker(m, i, &pb.SessionMarker{Origin: m.origin, Player: uuid.Bytes(), PlayerName: ps.name, Kind: kind,
		SessionId: ps.id, SessionStartUs: ps.startUs, HotLoaded: ps.hotLoaded, Exempt: ps.exempt,
		NetworkSession: ps.networkSession, GatewayRecvUnixUs: m.recvUs}, ps.join)
}

// buffer holds an event of an unknown session for at most 5 s (bounded).
func (e *engine) buffer(key sessKey, m *batchMeta, i int, ev *pb.Event, raw []byte) {
	t := int64(ev.GetTimeUs()) + m.offset
	b := e.buffers[key]
	if b == nil {
		b = &buffer{firstWall: e.env.now()}
		e.buffers[key] = b
	}
	if len(b.items) > 0 && t-b.items[0].t > bufferMaxAgeUs {
		e.env.m.Drops.WithLabelValues("unknown_session").Add(float64(len(b.items)))
		e.buffered -= len(b.items)
		b.items, b.firstWall = nil, e.env.now()
	}
	if len(b.items) >= bufferPerSession || e.buffered >= bufferPerPartition {
		e.env.m.Drops.WithLabelValues("buffer_full").Inc()
		return
	}
	b.items = append(b.items, bufItem{m: m, i: i, ev: ev, raw: append([]byte(nil), raw...), t: t})
	e.buffered++
}

func (e *engine) takeBuffer(key sessKey) []bufItem {
	b := e.buffers[key]
	if b == nil {
		return nil
	}
	delete(e.buffers, key)
	e.buffered -= len(b.items)
	newest := e.newestUs
	keep := b.items[:0]
	for _, it := range b.items {
		if newest-it.t <= bufferMaxAgeUs {
			keep = append(keep, it)
		} else {
			e.env.m.Drops.WithLabelValues("unknown_session").Inc()
		}
	}
	return keep
}

// ---------------------------------------------------------------------------------------------
// Events of a known session
// ---------------------------------------------------------------------------------------------

// apply runs an event of an open session, dropping events earlier than the session's last time.
func (e *engine) apply(s *session, m *batchMeta, i int, ev *pb.Event, raw []byte) {
	t := int64(ev.GetTimeUs()) + m.offset
	if t < s.lastT {
		e.env.m.Drops.WithLabelValues("stale").Inc()
		return
	}
	e.applyAt(s, m, i, ev, raw, t)
}

func (e *engine) applyAt(s *session, m *batchMeta, i int, ev *pb.Event, raw []byte, t int64) {
	s.lastT = max(s.lastT, t)
	s.lastSeen = e.env.now()
	s.origin = m.origin
	s.p.firstRecvUs = min(s.p.firstRecvUs, m.recvUs)
	wasExempt := s.exempt || s.bedrock
	switch k := ev.GetKind().(type) {
	case *pb.Event_Join:
		j := k.Join
		if j.GetName() != "" {
			s.name = j.GetName()
		}
		if j.GetSession() != "" {
			s.id = j.GetSession()
		}
		if j.GetNetworkSession() != "" {
			s.networkSession = j.GetNetworkSession()
		}
		s.exempt, s.bedrock = j.GetExempt(), j.GetBedrock()
		if j.GetBrand() != "" && s.join != nil {
			s.join.Brand = j.GetBrand()
		}
	case *pb.Event_State:
		s.exempt, s.bedrock = k.State.GetExempt(), k.State.GetBedrock()
	}
	if s.view != nil {
		s.view.Apply(ev, m.offset)
		if s.ring != nil {
			if raw == nil {
				raw, _ = proto.Marshal(ev)
			}
			switch ev.GetKind().(type) {
			case *pb.Event_Join:
				if !ev.GetJoin().GetUpdate() {
					s.joinRaw = append([]byte(nil), raw...)
				}
			case *pb.Event_State:
				s.stateRaw = append([]byte(nil), raw...)
			default:
				var target identity.UUID
				if a := ev.GetAttack(); a != nil && len(a.GetTargetPlayer()) == 16 {
					target, _ = identity.UUIDFromBytes(a.GetTargetPlayer())
				}
				e.account(s, s.ring.push(t, ev.GetTimeUs(), raw, target))
			}
		}
		if s.judged() {
			e.emits = e.emits[:0]
			s.ctx.Silent = m.silent
			s.ctx.SetEvent(ev)
			for _, d := range s.dets {
				e.runDetector(s, d, ev)
			}
			for _, em := range e.emits {
				e.signal(m, i, s, ev, t, em)
			}
			if s.features != nil && !m.silent && s.p.latest == s {
				e.windows(m, i, s, s.features.Observe(ev, s.view))
			}
		}
	}
	if f := ev.GetFlag(); f != nil && !s.bedrock {
		e.localSignal(m, i, s, ev, t, f)
	}
	if _, ok := ev.GetKind().(*pb.Event_State); ok && wasExempt != (s.exempt || s.bedrock) {
		e.marker(m, i, s, pb.SessionMarker_KIND_UPDATE)
	}
}

func (e *engine) runDetector(s *session, d detector, ev *pb.Event) {
	defer func() {
		if r := recover(); r != nil {
			e.env.detectorPanic(d.name, r)
		}
	}()
	d.d.Handle(s.ctx, ev)
}

// stall applies a ServerStall to the view of every session of its server in this partition
// (latency.blink reads View.Stalls) and to their feature windows; detectors do not see it.
func (e *engine) stall(m *batchMeta, i int, ev *pb.Event) {
	set := e.byServer[serverKey{m.tenant, m.server}]
	if len(set) == 0 {
		return
	}
	list := make([]*session, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].id < list[b].id })
	for _, s := range list {
		if s.closed || s.view == nil {
			continue
		}
		s.view.Apply(ev, m.offset)
		if s.features != nil && !m.silent && s.judged() && s.p.latest == s {
			e.windows(m, i, s, s.features.Observe(ev, s.view))
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------------------------

func (e *engine) ordinal(m *batchMeta, i int, check string) int {
	k := ordKey{m, i, check}
	n := e.ordinals[k]
	e.ordinals[k] = n + 1
	return n
}

func (e *engine) push(o outMsg) {
	o.subject = stream.SignalsSubject(e.part)
	e.out = append(e.out, o)
}

func (e *engine) probe(m *batchMeta, i int, ev *pb.Event, p *pb.Probe) {
	if m.silent {
		return
	}
	now := e.env.now()
	sig := &pb.Signal{Origin: m.origin, Player: ev.GetPlayer(), CheckId: checks.ProbeCheckID, EventTimeUs: ev.GetTimeUs(),
		GatewayRecvUnixUs: m.recvUs, WorkerUnixUs: uint64(now.UnixMicro()), Replayed: m.replayed, ProbeId: p.GetProbeId()}
	if m.recvUs > 0 {
		e.env.m.ProbeRTT.WithLabelValues(metrics.StageGatewayToWorker).Observe(float64(now.UnixMicro()-int64(m.recvUs)) / 1e6)
	}
	e.push(outMsg{msgID: stream.SignalMsgID(m.tenant, m.server, m.batchSeq, e.part, i, checks.ProbeCheckID, e.ordinal(m, i, checks.ProbeCheckID)),
		env: &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Signal{Signal: sig}}})
}

// signal turns a detector emission into a Signal (local = false).
func (e *engine) signal(m *batchMeta, i int, s *session, ev *pb.Event, t int64, em checks.Emission) {
	spec, _ := checks.Lookup(em.Check)
	sig := &pb.Signal{Origin: m.origin, Player: s.key.player.Bytes(), PlayerName: s.name, CheckId: em.Check,
		Category: spec.Category, Points: em.Points, Confidence: em.Confidence, EventTimeUs: em.EventTimeUs,
		GatewayRecvUnixUs: m.recvUs, WorkerUnixUs: uint64(e.env.now().UnixMicro()), Info: em.Info, Data: em.Data,
		Replayed: m.replayed, HotLoaded: s.hotLoaded, SessionId: s.id}
	e.emitSignal(m, i, s, t, sig)
}

// localSignal forwards a plugin LocalFlag (CHECKS.md section 3).
func (e *engine) localSignal(m *batchMeta, i int, s *session, ev *pb.Event, t int64, f *pb.LocalFlag) {
	points, conf, spec, ok := checks.LocalFlagScore(f)
	if !ok {
		e.env.m.Drops.WithLabelValues("unknown_local_check").Inc()
		return
	}
	data := make(map[string]float64, len(f.GetData())+2)
	for k, v := range f.GetData() {
		data[k] = v
	}
	data["vl"], data["severity"] = f.GetVl(), f.GetSeverity()
	info := f.GetInfo()
	if v := f.GetVariant(); v != "" {
		info = "[" + v + "] " + info
	}
	sig := &pb.Signal{Origin: m.origin, Player: s.key.player.Bytes(), PlayerName: s.name, CheckId: spec.ID,
		Category: spec.Category, Points: points, Confidence: conf, EventTimeUs: ev.GetTimeUs(),
		GatewayRecvUnixUs: m.recvUs, WorkerUnixUs: uint64(e.env.now().UnixMicro()), Info: info, Data: data, Local: true,
		Replayed: m.replayed, HotLoaded: s.hotLoaded, SessionId: s.id}
	e.emitSignal(m, i, s, t, sig)
}

func (e *engine) emitSignal(m *batchMeta, i int, s *session, t int64, sig *pb.Signal) {
	total := s.p.addPoints(t, sig.GetPoints())
	if m.silent {
		return
	}
	id := stream.SignalMsgID(m.tenant, m.server, m.batchSeq, e.part, i, sig.GetCheckId(), e.ordinal(m, i, sig.GetCheckId()))
	var job *clipJob
	if sig.GetPoints() >= clipPointsSingle || total >= clipPointsWindow {
		job = e.clip(m, s, t, id)
	}
	e.env.m.Signals.WithLabelValues(sig.GetCheckId()).Inc()
	e.push(outMsg{msgID: id, env: &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Signal{Signal: sig}}, clip: job})
}

// clip starts saving the last 25 s of the session as a replay clip for the signal msgID, when the
// limits allow it.
func (e *engine) clip(m *batchMeta, s *session, t int64, msgID string) *clipJob {
	if e.env.saver == nil || s.ring == nil || len(s.ring.items) == 0 {
		return nil
	}
	if !e.env.clips.allow(s.key.tenant, e.env.plan(s.key.tenant).clipsPerHour, s.p, t) {
		return nil
	}
	items := s.ring.since(t - clipSpanUs)
	if len(items) == 0 {
		return nil
	}
	job := &clipJob{origin: m.origin, player: s.key.player.Bytes(), startUs: items[0].plugin,
		endUs: items[len(items)-1].plugin, items: items, done: make(chan struct{})}
	for _, raw := range [][]byte{s.joinRaw, s.stateRaw} {
		if raw != nil {
			job.context = append(job.context, raw)
		}
	}
	subjects := map[string]bool{}
	for _, it := range items {
		if !it.target.IsZero() && it.target != s.key.player {
			subjects[it.target.String()] = true
		}
	}
	job.clip = store.Clip{ID: clipID(msgID), TenantID: s.key.tenant, ServerID: s.key.server, PlayerUUID: s.key.player.String(),
		StartUs: items[0].t, EndUs: items[len(items)-1].t, Codec: store.ClipCodecZstd, CreatedAt: e.env.now()}
	for sub := range subjects {
		job.clip.Subjects = append(job.clip.Subjects, sub)
	}
	sort.Strings(job.clip.Subjects)
	if !e.env.saver.submit(job) {
		return nil
	}
	return job
}

// marker publishes a SessionMarker for a backend session.
func (e *engine) marker(m *batchMeta, i int, s *session, kind pb.SessionMarker_Kind) {
	e.publishMarker(m, i, &pb.SessionMarker{Origin: m.origin, Player: s.key.player.Bytes(), PlayerName: s.name, Kind: kind,
		SessionId: s.id, SessionStartUs: s.startUs, HotLoaded: s.hotLoaded, Exempt: s.exempt || s.bedrock,
		NetworkSession: s.networkSession, GatewayRecvUnixUs: m.recvUs}, s.join)
}

func (e *engine) publishMarker(m *batchMeta, i int, sm *pb.SessionMarker, join *pb.PlayerJoin) {
	if m.silent {
		return
	}
	e.env.sessions.RecordSession(sm, join)
	e.push(outMsg{msgID: stream.SessionMarkerMsgID(m.tenant, m.server, m.batchSeq, e.part, i),
		env: &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Session{Session: sm}}})
}

// windows records finalised feature windows and scores them. m is nil for windows finalised by
// housekeeping or eviction (their ML signals get a window-based msg id).
func (e *engine) windows(m *batchMeta, i int, s *session, ws []*ml.Window) {
	if len(ws) == 0 || s.exempt || s.bedrock || (m != nil && m.silent) {
		return
	}
	for _, w := range ws {
		e.env.wm.Windows.Inc()
		e.env.windows.RecordFeatureWindow(telemetry.FeatureWindow{TenantID: w.TenantID, Player: identity.UUID(w.Player),
			ServerID: w.ServerID, SessionID: w.SessionID, WindowStartUs: w.WindowStartUs, WindowEndUs: w.WindowEndUs,
			ClientProtocol: w.ClientProtocol, TickClient: w.TickClient, HotLoaded: w.HotLoaded,
			FeatureVersion: w.FeatureVersion, Features: w.Features[:], WorkerUnixUs: w.WorkerUnixUs})
	}
	sc := e.env.scorer
	if sc == nil || !sc.Enabled() {
		return
	}
	var apply []*ml.Window
	for _, w := range ws {
		if sc.Applies(w) {
			apply = append(apply, w)
		}
	}
	if len(apply) == 0 {
		return
	}
	scores, err := sc.Score(apply)
	if err != nil {
		e.env.log.Warn("ml scoring failed", "err", err)
		return
	}
	origin := s.origin
	if m != nil {
		origin = m.origin
	}
	for k, w := range apply {
		if !scores[k].Flagged {
			continue
		}
		check := sc.CheckID()
		sig := &pb.Signal{Origin: origin, Player: s.key.player.Bytes(), PlayerName: s.name, CheckId: check,
			Category: pb.Category_CATEGORY_HEURISTICS, Points: sc.Points(), Confidence: scores[k].PCheat,
			EventTimeUs: uint64(w.WindowEndUs - origin.GetClockOffsetUs()), WorkerUnixUs: uint64(e.env.now().UnixMicro()),
			Info:      fmt.Sprintf("model P(cheat) %.3f over window %d", scores[k].PCheat, w.WindowStartUs/1_000_000),
			Data:      map[string]float64{"p_cheat": scores[k].PCheat, "window_start_s": float64(w.WindowStartUs / 1_000_000)},
			HotLoaded: s.hotLoaded, SessionId: s.id}
		var id string
		if m != nil && i >= 0 {
			sig.GatewayRecvUnixUs = m.recvUs
			id = stream.SignalMsgID(m.tenant, m.server, m.batchSeq, e.part, i, check, e.ordinal(m, i, check))
		} else {
			id = s.key.tenant + ":" + s.key.server + ":window:" + s.id + ":" + strconv.FormatInt(w.WindowStartUs, 10) + ":" + check
		}
		if m == nil || !m.silent {
			e.env.m.Signals.WithLabelValues(check).Inc()
			e.push(outMsg{msgID: id, env: &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Signal{Signal: sig}}})
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------------------------

// housekeep expires buffers, evicts idle sessions, finalises stale feature windows and drops erased
// players. It returns the messages it produced (ML signals of finalised windows).
func (e *engine) housekeep(now time.Time) []outMsg {
	for k, b := range e.buffers {
		if now.Sub(b.firstWall) > time.Duration(bufferMaxAgeUs)*time.Microsecond ||
			(len(b.items) > 0 && e.newestUs-b.items[0].t > bufferMaxAgeUs) {
			e.env.m.Drops.WithLabelValues("unknown_session").Add(float64(len(b.items)))
			e.buffered -= len(b.items)
			delete(e.buffers, k)
		}
	}
	for _, s := range e.sortedSessions() {
		switch {
		case s.closed && now.Sub(s.closedAt) > quitKeep, !s.closed && now.Sub(s.lastSeen) > silentEvict:
			e.removeSession(s, nil, -1)
		case s.features != nil && s.p.latest == s && s.judged():
			e.windows(nil, -1, s, s.features.Housekeep(e.newestUs))
		}
	}
	for k, ps := range e.proxies {
		if now.Sub(ps.lastSeen) > silentEvict {
			delete(e.proxies, k)
		}
	}
	for _, p := range e.players {
		if at, ok := e.env.erasures.Erased(p.key.tenant, p.key.player); ok && p.firstRecvUs <= uint64(at.UnixMicro()) {
			e.dropPlayer(p)
		}
	}
	return e.flush()
}

func (e *engine) sortedSessions() []*session {
	out := make([]*session, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].lastSeen.Equal(out[b].lastSeen) {
			return out[a].lastSeen.Before(out[b].lastSeen)
		}
		return out[a].id < out[b].id
	})
	return out
}

// shrink frees state of tenant's sessions in this partition, least recently active first: closed
// sessions, then sessions idle for a minute, then clip rings, then active sessions. It returns the
// bytes freed (at least need when possible).
func (e *engine) shrink(tenant string, need int64, now time.Time) int64 {
	var freed int64
	sessions := e.sortedSessions()
	phases := []func(s *session) bool{
		func(s *session) bool { return s.closed },
		func(s *session) bool { return now.Sub(s.lastSeen) > idleForEviction },
		nil, // clip rings
		func(s *session) bool { return true },
	}
	for _, match := range phases {
		for _, s := range sessions {
			if freed >= need {
				return freed
			}
			if s.key.tenant != tenant || e.sessions[s.key] != s {
				continue
			}
			if match == nil {
				if s.ring != nil && s.ring.bytes > 0 {
					d := s.ring.clear()
					e.account(s, d)
					freed -= d
				}
				continue
			}
			if match(s) {
				before := s.cost
				e.removeSession(s, nil, -1)
				freed += before
				e.env.m.Drops.WithLabelValues("memory_eviction").Inc()
			}
		}
	}
	return freed
}

// close releases everything the engine holds (partition handed over or stopping).
func (e *engine) close() {
	for _, s := range e.sortedSessions() {
		e.release(s)
	}
	e.sessions, e.players, e.byServer, e.buffers = map[sessKey]*session{}, map[playerKey]*player{}, map[serverKey]map[*session]struct{}{}, map[sessKey]*buffer{}
	e.flush()
}

// counts reports sessions and buffered events (metrics, tests).
func (e *engine) counts() (sessions, buffered int) { return len(e.sessions), e.buffered }
