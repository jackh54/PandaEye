package verdict

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

const (
	aA = pb.Action_ACTION_ALERT
	aM = pb.Action_ACTION_MITIGATE
	aK = pb.Action_ACTION_KICK
	aB = pb.Action_ACTION_BAN
)

func expect(t *testing.T, step string, fx effects, want ...pb.Action) {
	t.Helper()
	if got := actions(fx); !slices.Equal(got, want) {
		t.Fatalf("%s: actions %v, want %v", step, got, want)
	}
}

// ladderSequence drives a player up the whole ladder at leniency 4 (ALERT 19.5, MITIGATE 39,
// KICK 91, BAN 156) and returns each step's effects.
func ladderSequence(h *harness, tp *tplayer) []effects {
	return []effects{
		h.apply(tp.marker(pb.SessionMarker_KIND_JOIN)),
		h.apply(tp.signal("killaura.hitbox", 20)),                            // S 20: ALERT
		h.apply(tp.signal("reach.distance", 20)),                             // S 40: MITIGATE
		h.apply(tp.signal("reach.distance", 60)),                             // S 100, 2 families: KICK
		h.apply(tp.signal("killaura.noswing", 80, withClip("clip-noswing"))), // S 180, clip: BAN
		h.apply(tp.signal("killaura.noswing", 10)),                           // nothing new this session
	}
}

func TestActionLadder(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	fx := ladderSequence(h, tp)
	expect(t, "join", fx[0])
	expect(t, "alert", fx[1], aA)
	expect(t, "mitigate", fx[2], aM)
	expect(t, "kick", fx[3], aK)
	expect(t, "ban", fx[4], aB)
	expect(t, "once per session", fx[5])

	m := fx[2].verdicts[0].rec.Verdict
	if m.Shadow || m.Strength != 0.3 || len(m.Mitigations) != 3 || m.Mitigations[0] != pb.Mitigation_MITIGATION_HIT_CANCEL ||
		m.Category != pb.Category_CATEGORY_HEURISTICS {
		t.Fatalf("mitigate verdict %+v", m)
	}
	ban := fx[4].verdicts[0]
	v := ban.rec.Verdict
	if v.Shadow || v.AppealCode != identity.AppealCode(testOutKey, v.VerdictId) || v.AppealCode == "" {
		t.Fatalf("ban %+v", v)
	}
	if !strings.HasPrefix(v.Reason, "Combat heuristics (") || v.Checks[0] != "killaura.noswing" || v.EvidenceUrl != "https://dash.example/verdicts/"+v.VerdictId {
		t.Fatalf("ban fields %+v", v)
	}
	if ban.rec.ServerId != tp.server || ban.rec.SessionId != tp.session || ban.rec.NetworkId != "net-1" ||
		!slices.Equal(ban.rec.ClipIds, []string{"clip-noswing"}) || !ban.cloudCheck {
		t.Fatalf("ban record %+v", ban.rec)
	}
	if id, _ := identity.UUIDFromBytes(v.Player); id != tp.id || v.PlayerName != "Steve" {
		t.Fatal("player")
	}
	if want := identity.VerdictID(tp.tenant, tp.id, tp.session, int32(aB), 5); v.VerdictId != want {
		t.Fatalf("verdict id %s, want %s (seq 5)", v.VerdictId, want)
	}
	// ALERT again once a minute has passed and S grew 50 %.
	h.clk.add(61 * time.Second)
	expect(t, "re-alert", h.apply(tp.signal("aim.snap", 50)), aA)
}

func TestKickNeedsTwoFamilies(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	expect(t, "one family", h.apply(tp.signal("killaura.noswing", 80)), aA, aM) // S 80
	expect(t, "still one family", h.apply(tp.signal("killaura.hitbox", 40)))    // S 120 >= KICK, 1 family
	expect(t, "second family", h.apply(tp.signal("aim.snap", 5)), aK)           // aim 5
}

func TestShadowVerdictsWhenFlagsForbid(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant(func(s *stream.TenantSettings) { s.Autoban, s.Autokick, s.Mitigations = false, false, false }))
	fx := ladderSequence(h, tp)
	for i, want := range []pb.Action{aA, aM, aK, aB} {
		v := fx[i+1].verdicts[0].rec.Verdict
		if v.Action != want || v.Shadow != (want != aA) {
			t.Fatalf("step %d: %v shadow=%v", i, v.Action, v.Shadow)
		}
	}

	// Enforcement off (or a plugin in shadow mode): everything is shadow.
	h2 := newHarness(t)
	tp2 := h2.player(h2.tenant(func(s *stream.TenantSettings) { s.Enforcement = false }))
	for _, f := range ladderSequence(h2, tp2) {
		for _, v := range f.verdicts {
			if !v.rec.Verdict.Shadow {
				t.Fatalf("%v not shadow with enforcement off", v.rec.Verdict.Action)
			}
		}
	}
	h3 := newHarness(t)
	tp3 := h3.player(h3.tenant())
	tp3.shadow = true
	n := 0
	for _, f := range ladderSequence(h3, tp3) {
		for _, v := range f.verdicts {
			n++
			if !v.rec.Verdict.Shadow {
				t.Fatalf("%v not shadow for a shadow-mode plugin", v.rec.Verdict.Action)
			}
		}
	}
	if n != 4 {
		t.Fatalf("the full ladder must still be produced, got %d verdicts", n)
	}
}

func TestCertainCheckBansAlone(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	pitch := func(mod ...func(*pb.Signal)) effects {
		return h.apply(tp.signal("protocol.pitch", 25, append([]func(*pb.Signal){local()}, mod...)...))
	}
	expect(t, "25", pitch(withClip("clip-pitch")), aA)
	expect(t, "50", pitch(), aM)
	expect(t, "75 (< BAN/2 = 78)", pitch())
	fx := pitch() // acc 100 >= 78: BAN although S = 100 < BAN and only one family
	expect(t, "100", fx, aB)
	if sc := h.score(tp); sc.S >= ThresholdsFor(4).Ban || sc.FamiliesOver5 != 1 {
		t.Fatalf("certain path should be the only reason: %+v", sc)
	}
	if fx.verdicts[0].cloudCheck {
		t.Fatal("a local-only ban is not reputation eligible")
	}
}

func TestBanNeedsCommittedClip(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	for range 4 {
		h.apply(tp.signal("protocol.pitch", 25, local()))
	}
	for _, f := range []effects{h.apply(tp.signal("protocol.selfhit", 25, local()))} {
		if slices.Contains(actions(f), aB) {
			t.Fatal("BAN without a clip")
		}
	}
	expect(t, "clip arrives", h.apply(tp.signal("protocol.pitch", 1, local(), withClip("c"))), aB)
}

func TestBanNeedsConfidence(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	// Low-confidence signals: C = 1 - 0.5^3 < 0.9 despite a huge score.
	h.apply(tp.signal("killaura.hitbox", 80, withConf(0.5), withClip("c")))
	h.apply(tp.signal("reach.distance", 80, withConf(0.5)))
	fx := h.apply(tp.signal("velocity.vertical", 60, withConf(0.5)))
	if sc := h.score(tp); sc.S < 156 || sc.C >= 0.9 || sc.C < 0.8 {
		t.Fatalf("setup: %+v", sc)
	}
	if slices.Contains(actions(fx), aB) {
		t.Fatal("BAN below C 0.9")
	}
}

func TestStaleSignalsAlertAtMost(t *testing.T) {
	for name, mod := range map[string]func(*pb.Signal){
		"re-consumed (received 3 min ago)": withRecvAgo(3 * time.Minute),
		"catch-up (event 11 min old)":      withEventAgo(11 * time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			tp := h.player(h.tenant())
			h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
			fx := h.apply(tp.signal("protocol.pitch", 200, local(), withClip("c"), mod))
			expect(t, "stale", fx, aA)
			if len(fx.outbound) != 0 {
				t.Fatal("no watch request for stale signals")
			}
			// A fresh signal afterwards acts on the whole score.
			expect(t, "fresh", h.apply(tp.signal("protocol.pitch", 1, local())), aM, aB)
		})
	}
}

func TestExemptSessionsNeverAboveAlert(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	tp.exempt = true
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	fx := h.apply(tp.signal("protocol.pitch", 200, local(), withClip("c")))
	expect(t, "exempt", fx, aA)
}

func TestOverLimitTenantAlertOnly(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant(func(s *stream.TenantSettings) {
		s.OverLimitSince = time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	}))
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	expect(t, "over limit", h.apply(tp.signal("protocol.pitch", 200, local(), withClip("c"))), aA)
}

func TestLegitCoverHoldsBackKickAndBan(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	now := h.clk.now()
	if err := h.st.InsertLabel(context.Background(), &store.Label{TenantID: tp.tenant, PlayerUUID: tp.id.String(),
		Label: store.LabelLegit, Source: store.SourceManual, WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fx := ladderSequence(h, tp)
	var got []pb.Action
	for _, f := range fx {
		got = append(got, actions(f)...)
	}
	if !slices.Equal(got, []pb.Action{aA, aM}) {
		t.Fatalf("covered player got %v", got)
	}

	// An overturned verdict in the window covers too.
	h2 := newHarness(t)
	tp2 := h2.player(h2.tenant())
	old := &store.Verdict{ID: identity.NewUUID().String(), TenantID: tp2.tenant, ServerID: tp2.server, PlayerUUID: tp2.id.String(),
		Action: aB, CreatedAt: h2.clk.now()}
	ctx := context.Background()
	if _, err := h2.st.UpsertVerdict(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := h2.st.SetVerdictStatus(ctx, tp2.tenant, old.ID, store.StatusOverturned); err != nil {
		t.Fatal(err)
	}
	for _, f := range ladderSequence(h2, tp2) {
		if a := actions(f); slices.Contains(a, aK) || slices.Contains(a, aB) {
			t.Fatalf("overturned cover: %v", a)
		}
	}
}

func TestHotLoadedCountsHalf(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	tp.hot = true
	// Before the seed marker: half.
	h.apply(tp.signal("aim.snap", 10))
	if sc := h.score(tp); !near(sc.S, 5) {
		t.Fatalf("unseeded hot session: S %v", sc.S)
	}
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN)) // seeded now
	h.clk.add(30 * time.Second)
	before := h.score(tp).Checks[0].Acc
	h.apply(tp.signal("aim.snap", 10))
	if got := h.score(tp).Checks[0].Acc - before*decay(30, 300); !near(got, 5) {
		t.Fatalf("within 60 s of seeding: +%v", got)
	}
	h.clk.add(31 * time.Second)
	before = h.score(tp).Checks[0].Acc
	h.apply(tp.signal("aim.snap", 10))
	if got := h.score(tp).Checks[0].Acc - before*decay(31, 300); !near(got, 10) {
		t.Fatalf("61 s after seeding: +%v", got)
	}
}

func decay(seconds, halfLife float64) float64 { return math.Exp2(-seconds / halfLife) }

func TestRejoinReissuesMitigate(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	h.apply(tp.signal("killaura.hitbox", 30))
	first := h.apply(tp.signal("aim.snap", 20))
	expect(t, "mitigate", first, aM)
	h.apply(tp.marker(pb.SessionMarker_KIND_QUIT))
	tp.session = identity.NewUUID().String()
	fx := h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	expect(t, "rejoin", fx, aM)
	if fx.verdicts[0].rec.SessionId != tp.session || fx.verdicts[0].rec.Verdict.VerdictId == first.verdicts[0].rec.Verdict.VerdictId {
		t.Fatal("re-issued MITIGATE must belong to the new session")
	}
	expect(t, "not twice", h.apply(tp.signal("aim.snap", 1)))
}

func TestDeterministicVerdictIDs(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	var msgs []*stream.Msg
	for _, e := range []*pb.SignalEnvelope{tp.marker(pb.SessionMarker_KIND_JOIN), tp.signal("killaura.hitbox", 50),
		tp.signal("reach.distance", 60), tp.signal("killaura.noswing", 80, withClip("c"))} {
		msgs = append(msgs, h.msg(e))
	}
	ids := func(p *partition) []string {
		var out []string
		for _, m := range msgs {
			for _, v := range p.apply(m, true).verdicts {
				out = append(out, v.rec.Verdict.VerdictId)
			}
		}
		return out
	}
	a, b := ids(h.p), ids(newPartition(h.p.env, 0))
	if len(a) != 4 || !slices.Equal(a, b) {
		t.Fatalf("re-processing must reproduce the verdict ids: %v vs %v", a, b)
	}
}

func TestRedeliveredSignalsDoNotDoubleCount(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	e := tp.signal("aim.snap", 10)
	h.p.apply(h.msg(e, "worker-msg-1"), true)
	h.p.apply(h.msg(e, "worker-msg-1"), true) // same msg id, new stream sequence (republished)
	if sc := h.score(tp); !near(sc.S, 10) {
		t.Fatalf("S = %v after a duplicate", sc.S)
	}
	if n, _ := h.sink.counts(); n != 1 {
		t.Fatalf("telemetry recorded %d signals", n)
	}
}

func TestRebuildRestoresScoresWithoutActions(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	ctx := context.Background()
	for _, e := range []*pb.SignalEnvelope{tp.marker(pb.SessionMarker_KIND_JOIN), tp.signal("killaura.hitbox", 30), tp.signal("aim.snap", 20)} {
		if fx := h.p.apply(h.msg(e), false); len(fx.verdicts)+len(fx.outbound) > 0 {
			t.Fatalf("rebuild produced effects: %+v", fx)
		}
	}
	if sc := h.score(tp); !near(sc.S, 50) {
		t.Fatalf("restored S %v", sc.S)
	}
	if n, _ := h.sink.counts(); n != 0 {
		t.Fatal("rebuild must not record telemetry")
	}
	// The previous owner already mitigated this session (stored verdict).
	if _, err := h.st.UpsertVerdict(ctx, &store.Verdict{ID: identity.NewUUID().String(), TenantID: tp.tenant, ServerID: tp.server,
		PlayerUUID: tp.id.String(), SessionID: tp.session, Action: aM, CreatedAt: h.clk.now()}); err != nil {
		t.Fatal(err)
	}
	// Live again: no repeated ALERT (the ladder was rebuilt silently) nor MITIGATE (stored).
	expect(t, "after rebuild", h.apply(tp.signal("aim.snap", 1)))
	// The next threshold still fires.
	expect(t, "kick", h.apply(tp.signal("reach.distance", 60)), aK)
}

func TestRouting(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	fx := h.apply(tp.signal("killaura.hitbox", 30))
	if r := fx.verdicts[0].route; r.server != tp.server || r.lookupNetwork {
		t.Fatalf("open session route %+v", r)
	}
	// Offline (quit): a BAN looks for a connected server of the network, falling back to the newest
	// signal's server.
	h.apply(tp.marker(pb.SessionMarker_KIND_QUIT))
	for range 4 {
		fx = h.apply(tp.signal("protocol.pitch", 25, local(), withClip("c")))
	}
	ban := fx.verdicts[len(fx.verdicts)-1]
	if ban.rec.Verdict.Action != aB || !ban.route.lookupNetwork || ban.route.network != "net-1" || ban.route.fallback != tp.server {
		t.Fatalf("offline ban route %+v", ban.route)
	}
	// No MITIGATE/KICK for a closed session.
	for _, f := range []effects{fx} {
		if a := actions(f); slices.Contains(a, aM) || slices.Contains(a, aK) {
			t.Fatalf("closed session got %v", a)
		}
	}
	// Without a network id the newest signal's server is used directly.
	h2 := newHarness(t)
	tp2 := h2.player(h2.tenant())
	tp2.network = ""
	for range 4 {
		fx = h2.apply(tp2.signal("protocol.pitch", 25, local(), withClip("c")))
	}
	h2.apply(tp2.marker(pb.SessionMarker_KIND_QUIT))
	other := *tp2
	other.server, other.session = "11111111-2222-4333-8444-555555555555", identity.NewUUID().String()
	h2.apply(other.marker(pb.SessionMarker_KIND_JOIN))
	h2.apply(other.marker(pb.SessionMarker_KIND_QUIT))
	r := h2.p.route(h2.p.players[playerKey{tp2.tenant, tp2.id}], &session{id: "x"}, aB, trigger{origin: tp2.origin()})
	if r.lookupNetwork || r.server != other.server {
		t.Fatalf("no-network route %+v", r)
	}
}

func TestWatchRequests(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	if fx := h.apply(tp.signal("aim.snap", 5)); len(fx.outbound) != 0 {
		t.Fatal("below ALERT/2")
	}
	fx := h.apply(tp.signal("aim.snap", 5)) // 10 >= 9.75
	if len(fx.outbound) != 1 || fx.outbound[0].GetWatch().GetSeconds() != 60 || fx.outbound[0].ServerId != tp.server {
		t.Fatalf("watch %+v", fx.outbound)
	}
	if fx := h.apply(tp.signal("aim.snap", 1)); len(fx.outbound) != 0 {
		t.Fatal("watch repeated within 2 min")
	}
	h.clk.add(2 * time.Minute)
	if fx := h.apply(tp.signal("aim.snap", 10)); len(fx.outbound) != 1 {
		t.Fatal("watch after 2 min")
	}
}

func TestErasureDropsStateAndLateData(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.signal("aim.snap", 10))
	h.erased[playerKey{tp.tenant, tp.id}] = h.clk.now()
	late := tp.signal("aim.snap", 10, withRecvAgo(time.Second))
	if fx := h.apply(late); len(fx.verdicts) > 0 {
		t.Fatal("late data acted on")
	}
	if n := h.p.housekeep(); n != 1 {
		t.Fatalf("housekeeping evicted %d", n)
	}
	h.clk.add(time.Second)
	h.apply(tp.signal("aim.snap", 3)) // after the erasure: fresh state
	if sc := h.score(tp); !near(sc.S, 3) {
		t.Fatalf("state after erasure: %v", sc.S)
	}
}

func TestDisabledChecksAreIgnored(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant(func(s *stream.TenantSettings) {
		s.Checks = map[string]stream.CheckSettings{"aim.snap": {Enabled: false}}
	}))
	h.apply(tp.signal("aim.snap", 10))
	h.apply(tp.signal("autoclicker.cps", 10)) // off by default
	if sc := h.score(tp); sc.S != 0 {
		t.Fatalf("disabled checks scored: %v", sc.S)
	}
}

func TestProbeSignal(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	e := tp.signal("probe", 0, func(s *pb.Signal) {
		s.ProbeId, s.Player = 42, nil
		s.GatewayRecvUnixUs -= 3000
		s.WorkerUnixUs -= 1000
	})
	fx := h.apply(e)
	if len(fx.outbound) != 1 || len(fx.verdicts) != 0 {
		t.Fatalf("probe effects %+v", fx)
	}
	pr := fx.outbound[0].GetProbeResult()
	sig := e.GetSignal()
	if pr.ProbeId != 42 || pr.GatewayRecvUnixUs != sig.GatewayRecvUnixUs || pr.WorkerUnixUs != sig.WorkerUnixUs ||
		pr.VerdictUnixUs != uint64(h.clk.now().UnixMicro()) || fx.outbound[0].ServerId != tp.server {
		t.Fatalf("probe result %+v", pr)
	}
	if err := h.p.env.probes.flush(context.Background(), h.st); err != nil {
		t.Fatal(err)
	}
	stats, err := h.st.ListProbeStats(context.Background(), tp.tenant, tp.server, store.ProbeSourceCloud, h.clk.now().Add(-time.Hour))
	if err != nil || len(stats) != 1 || stats[0].Histogram.Count() != 1 || stats[0].MaxUs != 3000 {
		t.Fatalf("probe stats %+v %v", stats, err)
	}
	if fx := h.apply(tp.signal("probe", 0, withRecvAgo(3*time.Minute))); len(fx.outbound) != 0 {
		t.Fatal("re-consumed probe answered")
	}
	if fx := h.p.apply(h.msg(tp.signal("probe", 0)), false); len(fx.outbound) != 0 {
		t.Fatal("probe answered during rebuild")
	}
	if n, _ := h.sink.counts(); n != 0 {
		t.Fatal("probe signals are not telemetry rows")
	}
}

func TestEvidenceShape(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	fx := ladderSequence(h, tp)
	var ev map[string]any
	if err := json.Unmarshal([]byte(fx[4].verdicts[0].rec.EvidenceJson), &ev); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]any) []string {
		var k []string
		for key := range m {
			k = append(k, key)
		}
		slices.Sort(k)
		return k
	}
	check := func(what string, m map[string]any, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := keys(m); !slices.Equal(got, want) {
			t.Fatalf("%s keys %v, want %v", what, got, want)
		}
	}
	check("evidence", ev, "score", "confidence", "leniency", "thresholds", "families", "checks", "signals", "session")
	check("thresholds", ev["thresholds"].(map[string]any), "alert", "mitigate", "kick", "ban")
	check("family", ev["families"].([]any)[0].(map[string]any), "family", "contribution", "cap")
	check("check", ev["checks"].([]any)[0].(map[string]any), "check", "contribution", "cap", "confidence", "count")
	sigs := ev["signals"].([]any)
	check("signal", sigs[0].(map[string]any), "check", "category", "points", "confidence", "info", "data", "time", "clip_id", "local")
	check("session", ev["session"].(map[string]any), "session_id", "server_id", "started_at", "hot_loaded", "exempt")
	if ev["leniency"].(float64) != 4 || ev["thresholds"].(map[string]any)["ban"].(float64) != 156 || len(sigs) != 4 {
		t.Fatalf("evidence %v", ev)
	}
	if s := sigs[0].(map[string]any); s["category"] != "HEURISTICS" {
		t.Fatalf("signal category %v", s["category"])
	}
	if _, err := time.Parse(time.RFC3339, ev["session"].(map[string]any)["started_at"].(string)); err != nil {
		t.Fatal(err)
	}
}

func TestDiscordThreshold(t *testing.T) {
	ts := stream.TenantSettings{DiscordWebhookEnc: []byte{1}}
	if discordWanted(aA, ts) || !discordWanted(aM, ts) || !discordWanted(aB, ts) {
		t.Fatal("default min action is MITIGATE")
	}
	ts.DiscordMinAction = aB
	if discordWanted(aK, ts) || !discordWanted(aB, ts) {
		t.Fatal("min action BAN")
	}
	ts.DiscordMinAction = aA
	if !discordWanted(aA, ts) || discordWanted(pb.Action_ACTION_UNBAN, ts) {
		t.Fatal("min action ALERT")
	}
	if discordWanted(aB, stream.TenantSettings{}) {
		t.Fatal("no webhook, no post")
	}
}

func TestProxySessionsGetNoMitigation(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	h.apply(tp.signal("killaura.hitbox", 50))
	proxy := *tp
	proxy.server, proxy.session = testkit.NewServerID(), identity.NewUUID().String()
	m := proxy.marker(pb.SessionMarker_KIND_JOIN)
	m.GetSession().Origin.Platform = pb.Platform_PLATFORM_VELOCITY
	expect(t, "proxy join", h.apply(m))
}

// The live shadow BAN: block-interaction geometry alone (scaffold + dig, both experimental) reached
// the KICK and BAN ladders. Experimental families stop at MITIGATE until a stable family joins.
func TestExperimentalFamiliesAloneStopAtMitigate(t *testing.T) {
	h := newHarness(t)
	tp := h.player(h.tenant())
	h.apply(tp.marker(pb.SessionMarker_KIND_JOIN))
	expect(t, "scaffold", h.apply(tp.signal("scaffold.ray", 60, withClip("clip-s"))), aA, aM)
	expect(t, "dig: two experimental families, S 120 >= KICK", h.apply(tp.signal("dig.ray", 60)))
	expect(t, "more dig: S >= BAN, still experimental only", h.apply(tp.signal("scaffold.cursor", 60)))
	if sc := h.score(tp); sc.FamiliesOver5 != 2 || sc.StableOver5 != 0 || sc.S < ThresholdsFor(4).Ban {
		t.Fatalf("setup: %+v", sc)
	}
	expect(t, "a stable family corroborates", h.apply(tp.signal("aim.snap", 10)), aK, aB)
}
