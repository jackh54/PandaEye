package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

const testTenant = "t_aaaaaaaaaaaaaaaaaaaa"

// recorder is a fake telemetry sink.
type recorder struct {
	mu       sync.Mutex
	sessions []*pb.SessionMarker
	joins    []*pb.PlayerJoin
	windows  []telemetry.FeatureWindow
}

func (r *recorder) RecordSession(m *pb.SessionMarker, j *pb.PlayerJoin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, m)
	r.joins = append(r.joins, j)
}

func (r *recorder) RecordFeatureWindow(w telemetry.FeatureWindow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.windows = append(r.windows, w)
}

type mapTenants map[string]stream.TenantSettings

func (m mapTenants) Get(id string) (stream.TenantSettings, bool) { s, ok := m[id]; return s, ok }

type mapErasures map[string]time.Time

func (m mapErasures) Erased(tenant string, p identity.UUID) (time.Time, bool) {
	t, ok := m[tenant+"/"+p.String()]
	return t, ok
}

// memClips is a fake clip store.
type memClips struct {
	store.ClipStore
	mu    sync.Mutex
	clips map[string]*store.Clip
	gate  chan struct{} // when set, SaveClip waits for it
}

func (m *memClips) SaveClip(ctx context.Context, c *store.Clip) error {
	if m.gate != nil {
		select {
		case <-m.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clips == nil {
		m.clips = map[string]*store.Clip{}
	}
	if _, ok := m.clips[c.ID]; ok {
		return store.ErrConflict
	}
	cp := *c
	m.clips[c.ID] = &cp
	return nil
}

func (m *memClips) get(id string) *store.Clip {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clips[id]
}

type harness struct {
	t   *testing.T
	env *env
	eng *engine
	rec *recorder
	seq uint64
	ten mapTenants
	era mapErasures
}

func newHarness(t *testing.T, clips store.ClipStore) *harness {
	m := metrics.New()
	h := &harness{t: t, rec: &recorder{}, ten: mapTenants{}, era: mapErasures{}}
	h.env = &env{log: slog.New(slog.DiscardHandler), m: m, wm: newWorkerMetrics(m), factories: checks.Factories(),
		tenants: h.ten, erasures: h.era, budget: newBudget(0), clips: newClipPolicy(), sessions: h.rec, windows: h.rec,
		scorer: ml.Disabled{}, features: true, now: time.Now}
	if clips != nil {
		h.env.saver = newClipSaver(clips, h.env.log, m, h.env.wm.Clips)
		t.Cleanup(h.env.saver.close)
	}
	h.eng = newEngine(3, h.env, nil)
	return h
}

func origin(server string, platform pb.Platform) *pb.Origin {
	return &pb.Origin{TenantId: testTenant, ServerId: server, ServerName: "srv", Platform: platform}
}

// send handles one batch with the next stream sequence.
func (h *harness) send(o *pb.Origin, batchSeq uint64, silent bool, events ...*pb.Event) []outMsg {
	h.t.Helper()
	h.seq++
	sb := &pb.StreamBatch{Origin: o, BatchSeq: batchSeq, GatewayRecvUnixUs: uint64(time.Now().UnixMicro()), Events: events}
	data, err := proto.Marshal(sb)
	if err != nil {
		h.t.Fatal(err)
	}
	dec, raws, err := decodeBatch(data)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.eng.handle(h.seq, dec, raws, silent)
}

func signals(out []outMsg) []*pb.Signal {
	var s []*pb.Signal
	for _, m := range out {
		if sig := m.env.GetSignal(); sig != nil {
			s = append(s, sig)
		}
	}
	return s
}

func markers(out []outMsg) []*pb.SessionMarker {
	var s []*pb.SessionMarker
	for _, m := range out {
		if sm := m.env.GetSession(); sm != nil {
			s = append(s, sm)
		}
	}
	return s
}

func flag(p *testkit.Player, check string, sev float64) *pb.Event {
	p.Advance(10 * time.Millisecond)
	return p.Flag(check, pb.Category_CATEGORY_MOVEMENT, sev)
}

func TestSessionLifecycleAndLocalFlags(t *testing.T) {
	h := newHarness(t, nil)
	srv := testkit.NewServerID()
	o := origin(srv, pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("alice")
	out := h.send(o, 1, false, p.Join(), p.State())
	ms := markers(out)
	if len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_JOIN || ms[0].SessionId != p.Session || ms[0].PlayerName != "alice" {
		t.Fatalf("join markers %+v", ms)
	}
	if out[0].msgID != stream.SessionMarkerMsgID(testTenant, srv, 1, 3, 0) || out[0].subject != "pe.signals.03" {
		t.Fatalf("marker id %s subject %s", out[0].msgID, out[0].subject)
	}
	if len(h.rec.sessions) != 1 || h.rec.joins[0].GetProtocolVersion() != p.Protocol {
		t.Fatal("session marker not recorded to telemetry with its join")
	}
	out = h.send(o, 2, false, flag(p, "movement.speed", 0.5), flag(p, "protocol.slot", 1), flag(p, "movement.speed", 1))
	sigs := signals(out)
	if len(sigs) != 3 {
		t.Fatalf("signals %+v", sigs)
	}
	if s := sigs[0]; !s.Local || s.Points != 4 || s.Confidence != 0.75 || s.SessionId != p.Session || s.Category != pb.Category_CATEGORY_MOVEMENT {
		t.Fatalf("movement.speed flag %+v", s)
	}
	if sigs[1].Points != 10 || sigs[2].Points != 6 {
		t.Fatalf("points %v %v", sigs[1].Points, sigs[2].Points)
	}
	if out[2].msgID != stream.SignalMsgID(testTenant, srv, 2, 3, 2, "movement.speed", 0) {
		t.Fatalf("msg id %s", out[2].msgID)
	}
	redundant := flag(p, "protocol.slot", 0)
	redundant.GetFlag().Variant = "redundant"
	if sigs := signals(h.send(o, 3, false, redundant)); len(sigs) != 1 || sigs[0].Points != 0 {
		t.Fatalf("redundant slot change: %+v", sigs)
	}
	p.Advance(time.Second)
	out = h.send(o, 4, false, p.Quit(), flag(p, "movement.fly", 1))
	if ms := markers(out); len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_QUIT || len(signals(out)) != 0 {
		t.Fatalf("quit: %+v", out)
	}
}

func TestDuplicateBatchesAndStreamSequences(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("bob")
	h.send(o, 10, false, p.Join())
	f := flag(p, "movement.step", 0)
	if len(signals(h.send(o, 11, false, f))) != 1 {
		t.Fatal("first delivery")
	}
	if len(h.send(o, 11, false, f)) != 0 || len(h.send(o, 9, false, f)) != 0 {
		t.Fatal("a batch seq at or below the last one is a duplicate")
	}
	h.seq = 1 // a stream sequence already applied
	if len(h.send(o, 12, false, flag(p, "movement.step", 0))) != 0 {
		t.Fatal("stream sequences at or below the last applied are skipped")
	}
}

// Events earlier than the session's last normalised time are dropped.
func TestStaleEventsDropped(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("carol")
	h.send(o, 1, false, p.Join())
	late := flag(p, "movement.fly", 1)
	p.Advance(time.Second)
	if len(signals(h.send(o, 2, false, flag(p, "movement.fly", 1)))) != 1 {
		t.Fatal("in-order flag")
	}
	if len(signals(h.send(o, 3, false, late))) != 0 {
		t.Fatal("an event older than the session's last time must be dropped")
	}
}

// A restarted worker sees events before the plugin's periodic re-announcement (update join): they
// wait (<= 5 s) and run once the session is recovered, with their original msg ids.
func TestUnknownSessionBufferedUntilUpdateJoin(t *testing.T) {
	h := newHarness(t, nil)
	srv := testkit.NewServerID()
	o := origin(srv, pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("dave")
	early := flag(p, "movement.speed", 0)
	if out := h.send(o, 5, false, early); len(out) != 0 {
		t.Fatalf("no session info yet: %+v", out)
	}
	if _, n := h.eng.counts(); n != 1 {
		t.Fatalf("buffered %d", n)
	}
	update := p.Join()
	update.TimeUs = early.TimeUs + 100_000
	update.GetJoin().Update = true
	update.GetJoin().HotLoaded = true
	out := h.send(o, 6, false, update)
	ms, sigs := markers(out), signals(out)
	if len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_UPDATE || ms[0].SessionId != p.Session || !ms[0].HotLoaded {
		t.Fatalf("recovered session marker %+v", ms)
	}
	if len(sigs) != 1 || out[1].msgID != stream.SignalMsgID(testTenant, srv, 5, 3, 0, "movement.speed", 0) {
		t.Fatalf("buffered flag: %+v", out)
	}
	s := h.eng.sessions[sessKey{testTenant, identity.UUID(p.UUID), srv}]
	if s == nil || !s.view.TickClient || s.view.Protocol != p.Protocol {
		t.Fatal("a recovered session must know the client protocol")
	}
	// An update join for the known session changes nothing but metadata.
	again := p.Join()
	again.TimeUs = update.TimeUs + 1000
	again.GetJoin().Update, again.GetJoin().Brand = true, "fabric"
	if ms := markers(h.send(o, 7, false, again)); len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_UPDATE ||
		h.eng.sessions[sessKey{testTenant, identity.UUID(p.UUID), srv}] != s || s.join.GetBrand() != "fabric" {
		t.Fatal("update join must not reset the session")
	}
}

func TestUnknownSessionEventsExpire(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("erin")
	h.send(o, 1, false, flag(p, "movement.speed", 0))
	other := testkit.NewPlayer("other")
	other.TimeUs = p.TimeUs + 6_000_000
	h.send(o, 2, false, other.Join()) // the partition's clock moves 6 s on
	j := p.Join()
	j.TimeUs = p.TimeUs + 6_000_000
	j.GetJoin().Update = true
	if sigs := signals(h.send(o, 3, false, j)); len(sigs) != 0 {
		t.Fatalf("an event buffered longer than 5 s must be dropped: %+v", sigs)
	}
	h.send(o, 4, false, flag(other, "movement.fly", 0))
	h.eng.housekeep(time.Now().Add(10 * time.Second))
	if _, n := h.eng.counts(); n != 0 {
		t.Fatal("housekeeping expires buffers")
	}
}

// Velocity (proxy) events never touch backend sessions; they get their own markers.
func TestProxyEventsNeverTouchBackendSessions(t *testing.T) {
	h := newHarness(t, nil)
	backend := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	proxy := origin(testkit.NewServerID(), pb.Platform_PLATFORM_VELOCITY)
	p := testkit.NewPlayer("fay")
	pj := p.Join()
	pj.GetJoin().NetworkSession = identity.NewUUID().String()
	pj.GetJoin().Session = identity.NewUUID().String()
	out := h.send(proxy, 1, false, pj)
	if ms := markers(out); len(ms) != 1 || ms[0].Origin.Platform != pb.Platform_PLATFORM_VELOCITY || ms[0].Kind != pb.SessionMarker_KIND_JOIN {
		t.Fatalf("proxy join %+v", ms)
	}
	h.send(backend, 1, false, p.Join())
	p.Advance(time.Second)
	sw := &pb.Event{Player: p.UUID, TimeUs: p.TimeUs, Kind: &pb.Event_ServerSwitch{ServerSwitch: &pb.ServerSwitch{ToServer: "b"}}}
	if out := h.send(proxy, 2, false, sw, flag(p, "movement.speed", 0)); len(out) != 0 {
		t.Fatalf("proxy events produced %+v", out)
	}
	if out := h.send(proxy, 3, false, p.Quit()); len(markers(out)) != 1 {
		t.Fatal("proxy quit marker")
	}
	if sigs := signals(h.send(backend, 2, false, flag(p, "movement.speed", 0))); len(sigs) != 1 {
		t.Fatal("the backend session must survive the proxy quit")
	}
	if len(h.eng.sessions) != 1 || len(h.eng.proxies) != 0 {
		t.Fatalf("sessions %d proxies %d", len(h.eng.sessions), len(h.eng.proxies))
	}
}

// A server switch where the old server's quit arrives after the join on the new server.
func TestLateQuitFromPreviousServer(t *testing.T) {
	h := newHarness(t, nil)
	a := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	b := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("gus")
	h.send(a, 1, false, p.Join())
	pb2 := *p
	pb2.Session = identity.NewUUID().String()
	p.Advance(time.Second)
	pb2.TimeUs = p.TimeUs
	h.send(b, 1, false, pb2.Join())
	quit := p.Quit()
	out := h.send(a, 2, false, quit)
	if ms := markers(out); len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_QUIT || ms[0].Origin.ServerId != a.ServerId || ms[0].SessionId != p.Session {
		t.Fatalf("late quit %+v", ms)
	}
	pl := h.eng.players[playerKey{testTenant, identity.UUID(p.UUID)}]
	if pl == nil || pl.latest == nil || pl.latest.key.server != b.ServerId || pl.latest.closed {
		t.Fatal("the newer session on B must stay open and current")
	}
	pb2.Advance(time.Second)
	if len(signals(h.send(b, 2, false, flag(&pb2, "movement.speed", 0)))) != 1 {
		t.Fatal("B's events still run")
	}
	// A quit for a session this worker never saw still closes the server's session for the verdicts.
	c := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	if ms := markers(h.send(c, 1, false, p.Quit())); len(ms) != 1 || ms[0].SessionId != "" {
		t.Fatalf("unknown session quit %+v", ms)
	}
}

// reachCheat returns attacks on a target 4 blocks away (reach.distance fires from the second).
func reachCheat(p *testkit.Player, n int) []*pb.Event {
	target := testkit.NewPlayer("target")
	target.Pos = geom.V(0, 64, 4.3)
	target.TimeUs = p.TimeUs
	var evs []*pb.Event
	for i := 0; i < n; i++ {
		for k := 0; k < 25; k++ {
			evs = append(evs, p.Move(geom.Vec3{}, geom.Rotation{}))
		}
		target.TimeUs = p.TimeUs
		evs = append(evs, p.Attack(target), p.Swing())
	}
	return evs
}

func TestBedrockAndExemptSessions(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	normal := testkit.NewPlayer("n")
	h.send(o, 1, false, normal.Join(), normal.State())
	if n := len(signals(h.send(o, 2, false, reachCheat(normal, 4)...))); n == 0 {
		t.Fatal("fixture: reach must fire for a normal session")
	}
	bed := testkit.NewPlayer("b")
	j := bed.Join()
	j.GetJoin().Bedrock = true
	out := h.send(o, 3, false, j)
	if ms := markers(out); len(ms) != 1 || !ms[0].Exempt {
		t.Fatalf("bedrock marker %+v", ms)
	}
	if out := h.send(o, 4, false, append(reachCheat(bed, 4), flag(bed, "movement.fly", 1))...); len(out) != 0 {
		t.Fatalf("bedrock sessions are never judged: %+v", out)
	}
	ex := testkit.NewPlayer("e")
	j = ex.Join()
	j.GetJoin().Exempt = true
	h.send(o, 5, false, j)
	sigs := signals(h.send(o, 6, false, append(reachCheat(ex, 4), flag(ex, "movement.fly", 1))...))
	if len(sigs) != 1 || !sigs[0].Local {
		t.Fatalf("exempt sessions: no cloud checks, local flags forwarded: %+v", sigs)
	}
	// Exemption ending via StateChange is announced.
	st := ex.State()
	if ms := markers(h.send(o, 7, false, st)); len(ms) != 1 || ms[0].Kind != pb.SessionMarker_KIND_UPDATE || ms[0].Exempt {
		t.Fatalf("exemption change %+v", ms)
	}
}

func TestPlayerCapByPlan(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	var players []*testkit.Player
	for i := 0; i < 61; i++ {
		p := testkit.NewPlayer("p")
		players = append(players, p)
		if ms := markers(h.send(o, uint64(i+1), false, p.Join(), p.State())); len(ms) != 1 {
			t.Fatal("every session gets markers")
		}
	}
	last := players[60]
	if n := len(signals(h.send(o, 100, false, reachCheat(last, 4)...))); n != 0 {
		t.Fatalf("the 61st player of a free tenant is not tracked: %d signals", n)
	}
	if sigs := signals(h.send(o, 101, false, flag(last, "movement.fly", 1))); len(sigs) != 1 {
		t.Fatal("local flags of untracked players are still forwarded")
	}
	if _, players := h.env.budget.totals(); players != 60 {
		t.Fatalf("tracked %d", players)
	}
	h.ten[testTenant] = stream.TenantSettings{TenantID: testTenant, Plan: "pro"}
	p := testkit.NewPlayer("paid")
	h.send(o, 102, false, p.Join(), p.State())
	if n := len(signals(h.send(o, 103, false, reachCheat(p, 4)...))); n == 0 {
		t.Fatal("paid plans are not capped")
	}
}

// Tenant check configuration from PE_TENANTS: a disabled check emits nothing.
func TestTenantCheckConfig(t *testing.T) {
	h := newHarness(t, nil)
	h.ten[testTenant] = stream.TenantSettings{TenantID: testTenant, Checks: map[string]stream.CheckSettings{
		checks.ReachCheck: {Enabled: false}}}
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("p")
	h.send(o, 1, false, p.Join(), p.State())
	for _, s := range signals(h.send(o, 2, false, reachCheat(p, 4)...)) {
		if s.CheckId == checks.ReachCheck {
			t.Fatal("disabled check emitted")
		}
	}
}

func TestProbeSignal(t *testing.T) {
	h := newHarness(t, nil)
	srv := testkit.NewServerID()
	out := h.send(origin(srv, pb.Platform_PLATFORM_PAPER), 1, false, testkit.Probe(42, uint64(time.Now().UnixMicro())))
	sigs := signals(out)
	if len(sigs) != 1 || sigs[0].CheckId != "probe" || sigs[0].ProbeId != 42 || sigs[0].Points != 0 || sigs[0].WorkerUnixUs == 0 {
		t.Fatalf("probe %+v", sigs)
	}
	if out[0].msgID != stream.SignalMsgID(testTenant, srv, 1, 3, 0, "probe", 0) {
		t.Fatal(out[0].msgID)
	}
}

func TestWarmModeEmitsNothing(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("w")
	evs := append([]*pb.Event{p.Join(), p.State()}, reachCheat(p, 4)...)
	evs = append(evs, flag(p, "movement.fly", 1), testkit.Probe(1, p.TimeUs))
	if out := h.send(o, 1, true, evs...); len(out) != 0 {
		t.Fatalf("warm start emitted %+v", out)
	}
	if len(h.rec.sessions) != 0 || len(h.rec.windows) != 0 {
		t.Fatal("warm start recorded telemetry")
	}
	if h.eng.sessions[sessKey{testTenant, identity.UUID(p.UUID), o.ServerId}] == nil {
		t.Fatal("warm start must rebuild the session")
	}
	if sigs := signals(h.send(o, 2, false, reachCheat(p, 1)...)); len(sigs) == 0 {
		t.Fatal("after the warm start the windows are warm: the next far attack violates at once")
	}
}

func TestErasureDropsState(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("gone")
	h.send(o, 1, false, p.Join(), p.State(), flag(p, "movement.fly", 1))
	h.era[testTenant+"/"+identity.UUID(p.UUID).String()] = time.Now().Add(time.Second)
	if out := h.send(o, 2, false, flag(p, "movement.fly", 1)); len(out) != 0 {
		t.Fatalf("late data of an erased player: %+v", out)
	}
	if len(h.eng.sessions) != 0 || len(h.eng.players) != 0 {
		t.Fatal("erasure must drop the player's state")
	}
	// Housekeeping drops state of a player erased while idle.
	q := testkit.NewPlayer("idle")
	h.send(o, 3, false, q.Join())
	h.era[testTenant+"/"+identity.UUID(q.UUID).String()] = time.Now().Add(time.Second)
	h.eng.housekeep(time.Now())
	if len(h.eng.sessions) != 0 {
		t.Fatal("housekeeping must drop erased players")
	}
}

func TestStallAppliedToEverySessionOfTheServer(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	other := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	a, b, c := testkit.NewPlayer("a"), testkit.NewPlayer("b"), testkit.NewPlayer("c")
	h.send(o, 1, false, a.Join(), b.Join())
	h.send(other, 1, false, c.Join())
	now := uint64(time.Now().UnixMicro())
	h.send(o, 2, false, testkit.Stall(now-600_000, now-100_000, 0.5))
	for _, p := range []*testkit.Player{a, b} {
		if s := h.eng.sessions[sessKey{testTenant, identity.UUID(p.UUID), o.ServerId}]; s.view.Stalls.Len() != 1 {
			t.Fatalf("%s: stall not applied", p.Name)
		}
	}
	if s := h.eng.sessions[sessKey{testTenant, identity.UUID(c.UUID), other.ServerId}]; s.view.Stalls.Len() != 0 {
		t.Fatal("stalls of another server")
	}
}

func TestClipSavedAndAttachedAfterCommit(t *testing.T) {
	st := &memClips{}
	h := newHarness(t, st)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("clip")
	h.send(o, 1, false, p.Join(), p.State())
	var evs []*pb.Event
	for i := 0; i < 40; i++ {
		evs = append(evs, p.Move(geom.V(0.1, 0, 0), geom.Rotation{}))
	}
	h.send(o, 2, false, evs...)
	out := h.send(o, 3, false, flag(p, "protocol.pitch", 1)) // 25 points
	if len(out) != 1 || out[0].clip == nil {
		t.Fatalf("a 25-point signal must start a clip: %+v", out)
	}
	job := out[0].clip
	<-job.done
	c := st.get(job.clip.ID)
	if !job.committed || c == nil || c.Codec != store.ClipCodecZstd || c.PlayerUUID != identity.UUID(p.UUID).String() {
		t.Fatalf("clip %+v committed %v", c, job.committed)
	}
	if job.clip.ID != clipID(out[0].msgID) {
		t.Fatal("clip ids derive from the signal msg id")
	}
	dec, _ := codec.NewDecoder(codec.Limits{})
	defer dec.Close()
	raw, err := dec.DecodeZstd(context.Background(), c.Data, codec.MaxDecompressed)
	if err != nil {
		t.Fatal(err)
	}
	var cd pb.ClipData
	if err := proto.Unmarshal(raw, &cd); err != nil {
		t.Fatal(err)
	}
	if cd.FormatVersion != 1 || len(cd.Events) != 43 || cd.Events[0].GetJoin() == nil || cd.Events[1].GetState() == nil ||
		cd.Events[42].GetFlag() == nil || cd.StartUs >= cd.EndUs {
		t.Fatalf("clip data: %d events, %v..%v", len(cd.Events), cd.StartUs, cd.EndUs)
	}
	// At most one clip per player per 60 s.
	if out := h.send(o, 4, false, flag(p, "protocol.pitch", 1)); out[0].clip != nil {
		t.Fatal("second clip within 60 s")
	}
	// Points in the last 60 s reaching 10 trigger a clip too (for another player).
	q := testkit.NewPlayer("q")
	h.send(o, 5, false, q.Join(), q.Move(geom.Vec3{}, geom.Rotation{}))
	var last []outMsg
	for i := 0; i < 4; i++ {
		last = h.send(o, uint64(6+i), false, flag(q, "protocol.cursor", 1)) // 3 points each
	}
	if last[0].clip == nil {
		t.Fatal("12 points within 60 s must start a clip")
	}
}

func TestClipBudgetPerTenant(t *testing.T) {
	c := newClipPolicy()
	now := int64(1_000_000_000_000)
	for i := 0; i < 30; i++ {
		if !c.allow(testTenant, freeLimits.clipsPerHour, &player{}, now+int64(i)) {
			t.Fatalf("clip %d refused", i)
		}
	}
	if c.allow(testTenant, freeLimits.clipsPerHour, &player{}, now+100) {
		t.Fatal("free tenants get 30 clips per hour")
	}
	if !c.allow(testTenant, freeLimits.clipsPerHour, &player{}, now+int64(time.Hour/time.Microsecond)+1000) {
		t.Fatal("the budget refills after an hour")
	}
}

func TestMemoryBudgetEvictsLRUOfHeaviestTenant(t *testing.T) {
	h := newHarness(t, nil)
	h.env.budget = newBudget(3 * trackedSessionBytes)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	var ps []*testkit.Player
	for i := 0; i < 5; i++ {
		p := testkit.NewPlayer("m")
		ps = append(ps, p)
		h.send(o, uint64(i+1), false, p.Join())
	}
	oldest := h.eng.sessions[sessKey{testTenant, identity.UUID(ps[0].UUID), o.ServerId}]
	oldest.lastSeen = time.Now().Add(-2 * time.Minute)
	over := h.env.budget.over()
	if over <= 0 {
		t.Fatal("fixture must be over budget")
	}
	freed := h.eng.shrink(testTenant, over, time.Now())
	h.eng.flush()
	if freed < over || h.env.budget.over() != 0 {
		t.Fatalf("freed %d of %d", freed, over)
	}
	if h.eng.sessions[oldest.key] != nil {
		t.Fatal("the idle session goes first")
	}
}

func TestIdleEviction(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	a, b := testkit.NewPlayer("a"), testkit.NewPlayer("b")
	h.send(o, 1, false, a.Join(), b.Join())
	a.Advance(time.Second)
	h.send(o, 2, false, a.Quit())
	h.eng.housekeep(time.Now().Add(11 * time.Minute))
	if len(h.eng.sessions) != 1 {
		t.Fatalf("10 min after the quit the session goes: %d left", len(h.eng.sessions))
	}
	h.eng.housekeep(time.Now().Add(16 * time.Minute))
	if len(h.eng.sessions) != 0 {
		t.Fatal("15 min silent sessions go")
	}
	if bytes, players := h.env.budget.totals(); bytes != 0 || players != 0 {
		t.Fatalf("budget not released: %d bytes, %d players", bytes, players)
	}
}

// A detector that panics never takes the worker down.
func TestDetectorPanicIsContained(t *testing.T) {
	h := newHarness(t, nil)
	h.env.factories = append(h.env.factories, checks.Factory{Name: "zz-panic", Checks: []string{checks.ReachCheck},
		New: func() checks.Detector { return panicDetector{} }})
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("x")
	h.send(o, 1, false, p.Join())
	if sigs := signals(h.send(o, 2, false, flag(p, "movement.fly", 1))); len(sigs) != 1 || h.env.panics.Load() == 0 {
		t.Fatal("panic not contained")
	}
}

type panicDetector struct{}

func (panicDetector) Handle(*checks.Ctx, *pb.Event) { panic("boom") }

func TestFeatureWindowsRecorded(t *testing.T) {
	h := newHarness(t, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("fw")
	h.send(o, 1, false, p.Join(), p.State())
	var evs []*pb.Event
	for i := 0; i < 700; i++ { // 35 s of movement: one full window plus G
		evs = append(evs, p.Move(geom.V(0.05, 0, 0), geom.Rotation{Yaw: float64(i % 30)}))
	}
	h.send(o, 2, false, evs...)
	h.rec.mu.Lock()
	n := len(h.rec.windows)
	var w telemetry.FeatureWindow
	if n > 0 {
		w = h.rec.windows[0]
	}
	h.rec.mu.Unlock()
	if n == 0 || w.FeatureVersion != 1 || len(w.Features) != ml.NumFeatures || w.SessionID != p.Session || !w.TickClient {
		t.Fatalf("windows %d %+v", n, w)
	}
	if w.Features[ml.FMoveCount] == 0 {
		t.Fatal("window without moves")
	}
	h.send(o, 3, false, p.Quit())
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	if len(h.rec.windows) != n+1 {
		t.Fatalf("the quit finalises the open window: %d -> %d", n, len(h.rec.windows))
	}
}
