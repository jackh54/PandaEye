package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// storeVerdict inserts a verdict as the store writer does.
func storeVerdict(t *testing.T, e *env, tenantID, serverID string, action pb.Action, age time.Duration) *pb.Verdict {
	t.Helper()
	v := testVerdict(action)
	created := time.Now().Add(-age)
	v.CreatedUnixMs = uint64(created.UnixMilli())
	payload, err := proto.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := identity.UUIDFromBytes(v.Player)
	if _, err := e.d.Store.UpsertVerdict(context.Background(), &store.Verdict{ID: v.VerdictId, TenantID: tenantID, ServerID: serverID,
		PlayerUUID: u.String(), PlayerName: v.PlayerName, Action: action, Score: v.Score, Confidence: v.Confidence, Reason: v.Reason,
		Checks: v.Checks, Payload: payload, CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	return v
}

// readVerdicts reads until n verdicts arrived (other frames are skipped).
func readVerdicts(t *testing.T, c *testkit.Client, n int) []*pb.Verdict {
	t.Helper()
	ctx := ctxT(t, 10*time.Second)
	var out []*pb.Verdict
	for len(out) < n {
		v, err := c.ReadVerdict(ctx)
		if err != nil {
			t.Fatalf("after %d verdicts: %v", len(out), err)
		}
		out = append(out, v)
	}
	return out
}

func status(t *testing.T, e *env, tenantID, id string) *store.Verdict {
	t.Helper()
	v, err := e.d.Store.GetVerdict(context.Background(), tenantID, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPendingVerdictsSweepAndRevalidation(t *testing.T) {
	e := newEnv(t, fastOptions())
	ctx := context.Background()
	s := newServer(keyPro)
	c, w := e.connect(s)
	c.Close()
	tenant := w.TenantId
	e.patchTenant(tenant, store.TenantPatch{Enforcement: ptr(true), Autoban: ptr(true), Autokick: ptr(false), Mitigations: ptr(false)})

	unban := storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_UNBAN, 48*time.Hour) // never expires
	ban := storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_BAN, time.Hour)
	storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_KICK, 10*time.Minute) // too old to kick
	kick := storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_KICK, time.Minute)
	mitigate := storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_MITIGATE, 30*time.Second)
	alert := storeVerdict(t, e, tenant, s.id, pb.Action_ACTION_ALERT, 10*time.Second)
	storeVerdict(t, e, tenant, testkit.NewServerID(), pb.Action_ACTION_BAN, time.Minute) // another server's

	c, _ = e.connect(s)
	got := readVerdicts(t, c, 4)
	firstAlert := time.Now()
	want := []struct {
		v      *pb.Verdict
		shadow bool
	}{{unban, false}, {ban, false}, {kick, true}, {alert, false}}
	for i, x := range want {
		if got[i].VerdictId != x.v.VerdictId || got[i].Shadow != x.shadow {
			t.Fatalf("verdict %d: got %v %v shadow=%v, want %v shadow=%v", i, got[i].Action, got[i].VerdictId, got[i].Shadow, x.v.Action, x.shadow)
		}
	}
	testkit.Eventually(t, 5*time.Second, "mitigate expired", func() bool { return status(t, e, tenant, mitigate.VerdictId).Status == store.StatusExpired })
	testkit.Eventually(t, 5*time.Second, "delivered_at", func() bool { return !status(t, e, tenant, ban.VerdictId).DeliveredAt.IsZero() })

	// Acks are relayed to pe.acks.<tenant>; the store writer (simulated here) applies them.
	for _, v := range []*pb.Verdict{unban, ban, kick} {
		if err := c.AckVerdict(ctxT(t, 5*time.Second), v.VerdictId, v == ban, "test"); err != nil {
			t.Fatal(err)
		}
	}
	subj, _ := stream.AcksSubject(tenant)
	var acks []testkit.StoredMsg
	testkit.Eventually(t, 5*time.Second, "ack records", func() bool {
		acks = testkit.ReadStream(t, e.d.Bus, stream.StreamVerdicts, subj)
		return len(acks) == 3
	})
	for _, m := range acks {
		var a pb.AckRecord
		if err := proto.Unmarshal(m.Data, &a); err != nil || a.TenantId != tenant || a.ServerId != s.id || m.MsgID != stream.AckRecordMsgID(a.VerdictId) {
			t.Fatalf("ack record %+v %s %v", &a, m.MsgID, err)
		}
		if _, err := e.d.Store.ApplyAck(ctx, store.Ack{TenantID: tenant, ServerID: s.id, VerdictID: a.VerdictId, Executed: a.Executed, Note: a.Note}); err != nil {
			t.Fatal(err)
		}
	}

	// The unacknowledged ALERT comes back after ResendAfter, never sooner; acked ones do not.
	again := readVerdicts(t, c, 1)[0]
	if again.VerdictId != alert.VerdictId || time.Since(firstAlert) < 350*time.Millisecond {
		t.Fatalf("redelivered %v after %v", again.VerdictId, time.Since(firstAlert))
	}

	// Kill switch: with enforcement off, everything is delivered as shadow.
	e.patchTenant(tenant, store.TenantPatch{Enforcement: ptr(false)})
	if _, err := c.ReadUntil(ctxT(t, 5*time.Second), func(f *pb.ServerFrame) bool {
		return f.GetConfigUpdate() != nil && !f.GetConfigUpdate().Enforcement
	}); err != nil {
		t.Fatalf("config update: %v", err)
	}
	live := testVerdict(pb.Action_ACTION_BAN)
	publishVerdict(t, e, tenant, s.id, live)
	for seen := map[string]bool{}; !seen[live.VerdictId] || !seen[alert.VerdictId]; {
		v := readVerdicts(t, c, 1)[0]
		if !v.Shadow {
			t.Fatalf("%v delivered enforceable with enforcement off", v.Action)
		}
		if v.VerdictId != live.VerdictId && v.VerdictId != alert.VerdictId {
			t.Fatalf("unexpected verdict %v", v.Action)
		}
		seen[v.VerdictId] = true
	}
}

func TestSignedOutbound(t *testing.T) {
	next := string(identity.RandomKey(32))
	e := newEnv(t, fastOptions(), "PE_OUTBOUND_KEY_NEXT="+next)
	s := newServer(keyPro)
	c, w := e.connect(s)
	other := newServer(keyOther)
	_, wo := e.connect(other)
	subj, _ := stream.OutSubject(w.TenantId, s.id)
	send := func(key []byte, out *pb.Outbound) {
		t.Helper()
		data, err := stream.EncodeOutbound(key, out)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.d.Bus.NC.Publish(subj, data); err != nil {
			t.Fatal(err)
		}
	}
	verdictTo := func(tenant, server string) (*pb.Outbound, *pb.Verdict) {
		v := testVerdict(pb.Action_ACTION_ALERT)
		return &pb.Outbound{TenantId: tenant, ServerId: server, Kind: &pb.Outbound_Verdict{Verdict: v}}, v
	}
	key := e.d.Secrets.OutboundKey.Bytes()
	bad, _ := verdictTo(w.TenantId, s.id)
	send(identity.RandomKey(32), bad) // wrong key
	crossTenant, _ := verdictTo(wo.TenantId, s.id)
	send(key, crossTenant) // signed, but for another tenant
	crossServer, _ := verdictTo(w.TenantId, other.id)
	send(key, crossServer) // signed, but for another server
	if err := e.d.Bus.NC.Publish(subj, []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	rotated, vr := verdictTo(w.TenantId, s.id)
	send([]byte(next), rotated) // the rotation key is accepted
	good, vg := verdictTo(w.TenantId, s.id)
	send(key, good)

	got := readVerdicts(t, c, 2)
	if got[0].VerdictId != vr.VerdictId || got[1].VerdictId != vg.VerdictId || !got[1].Shadow {
		t.Fatalf("delivered %v %v", got[0].VerdictId, got[1].VerdictId)
	}
	if n := counterValue(e.d.Metrics.Drops.WithLabelValues("outbound_rejected")); n != 4 {
		t.Fatalf("rejected %v outbound messages, want 4", n)
	}

	// Other kinds pass through; probe results get the gateway send time.
	recv := uint64(time.Now().Add(-30 * time.Millisecond).UnixMicro())
	p := testkit.NewPlayerID()
	for _, out := range []*pb.Outbound{
		{Kind: &pb.Outbound_ProbeResult{ProbeResult: &pb.ProbeResult{ProbeId: 9, GatewayRecvUnixUs: recv, VerdictUnixUs: recv + 1000}}},
		{Kind: &pb.Outbound_Watch{Watch: &pb.WatchRequest{Player: p, Seconds: 60}}},
		{Kind: &pb.Outbound_PlayerRisk{PlayerRisk: &pb.PlayerRisk{Player: p, Risk: 0.5}}},
		{Kind: &pb.Outbound_ConfigUpdate{ConfigUpdate: &pb.RemoteConfig{Leniency: 2}}},
		{Kind: &pb.Outbound_CloseConnectionId{CloseConnectionId: identity.NewUUID().String()}}, // not this connection
	} {
		out.TenantId, out.ServerId = w.TenantId, s.id
		publishOutbound(t, e, out)
	}
	ctx := ctxT(t, 5*time.Second)
	f, err := c.ReadUntil(ctx, func(f *pb.ServerFrame) bool { return f.GetProbeResult() != nil })
	if err != nil || f.GetProbeResult().ProbeId != 9 || f.GetProbeResult().GatewaySendUnixUs < recv {
		t.Fatalf("probe result %v %v", f, err)
	}
	for _, match := range []func(*pb.ServerFrame) bool{
		func(f *pb.ServerFrame) bool { return f.GetWatch().GetSeconds() == 60 },
		func(f *pb.ServerFrame) bool { return f.GetPlayerRisk().GetRisk() == 0.5 },
		func(f *pb.ServerFrame) bool { return f.GetConfigUpdate().GetLeniency() == 2 },
	} {
		if _, err := c.ReadUntil(ctx, match); err != nil {
			t.Fatal(err)
		}
	}
	publishOutbound(t, e, &pb.Outbound{TenantId: w.TenantId, ServerId: s.id, Kind: &pb.Outbound_CloseConnectionId{CloseConnectionId: w.ConnectionId}})
	if _, code := c.ReadErr(ctx); code != 1000 {
		t.Fatalf("close_connection_id: close code %d", code)
	}
}

func TestOutboundQueueOverflowCloses(t *testing.T) {
	o := fastOptions()
	o.OutboundQueue = 5
	e := newEnv(t, o)
	s := newServer(keyPro)
	c, w := e.connect(s)
	// The client does not read: the writer blocks on the socket once its buffers fill.
	big := testVerdict(pb.Action_ACTION_ALERT)
	big.Reason = string(make([]byte, 200_000))
	for i := 0; i < 200; i++ {
		v := proto.Clone(big).(*pb.Verdict)
		v.VerdictId = identity.NewUUID().String()
		publishVerdict(t, e, w.TenantId, s.id, v)
	}
	testkit.Eventually(t, 10*time.Second, "overflow", func() bool {
		return counterValue(e.d.Metrics.Drops.WithLabelValues("outbound_overflow")) >= 1
	})
	if _, code := c.ReadErr(ctxT(t, 10*time.Second)); code != 1011 {
		t.Fatalf("close code %d", code)
	}
}

func TestPlayerRiskOnJoin(t *testing.T) {
	e := newEnv(t, fastOptions())
	ctx := context.Background()
	s := newServer(keyPro)
	c, w := e.connect(s)
	tenant := w.TenantId
	cheater, rookie, clean := testkit.NewPlayer("Cheater"), testkit.NewPlayer("Rookie"), testkit.NewPlayer("Clean")
	for i, other := range []string{identity.NewTenantID(), identity.NewTenantID()} {
		if err := e.d.Store.CreateTenant(ctx, store.NewTenant(other, "other", "other-"+other[2:], time.Now())); err != nil {
			t.Fatal(err)
		}
		players := []*testkit.Player{cheater}
		if i == 0 {
			players = append(players, rookie) // flagged by one network only: no risk
		}
		for _, p := range players {
			u, _ := identity.UUIDFromBytes(p.UUID)
			if err := e.d.Store.AddReputationContrib(ctx, store.ReputationContrib{TenantID: other, PlayerUUID: u.String(),
				VerdictID: identity.NewUUID().String(), Weight: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Not opted in: no lookups.
	sendBatch(t, c, testkit.Batch(1, cheater.Join()))
	e.patchTenant(tenant, store.TenantPatch{ReputationOptIn: ptr(true)})
	update := cheater.Join()
	update.GetJoin().Update = true
	sendBatch(t, c, testkit.Batch(2, update, rookie.Join(), clean.Join(), cheater.Join()))
	f, err := c.ReadUntil(ctxT(t, 5*time.Second), func(f *pb.ServerFrame) bool { return f.GetPlayerRisk() != nil })
	if err != nil {
		t.Fatal(err)
	}
	r := f.GetPlayerRisk()
	if string(r.Player) != string(cheater.UUID) || r.NetworksFlagged != 2 || r.ConfirmedBans != 2 || r.Risk != 0.75 {
		t.Fatalf("risk %+v", r)
	}
	u, _ := identity.UUIDFromBytes(clean.UUID)
	if p, err := e.d.Store.GetPlayer(ctx, tenant, u.String()); err != nil || p.LastName != "Clean" || p.LastServerID != s.id {
		t.Fatalf("player row %+v %v", p, err)
	}
	expectNoFrame(t, c, 300*time.Millisecond, func(f *pb.ServerFrame) bool { return f.GetPlayerRisk() != nil })
}

func TestRevalidate(t *testing.T) {
	on := stream.TenantSettings{Enforcement: true, Autoban: true, Autokick: true, Mitigations: true}
	for _, tc := range []struct {
		action    pb.Action
		shadow    bool
		settings  func(*stream.TenantSettings)
		ok, wantS bool
	}{
		{pb.Action_ACTION_BAN, false, nil, true, false},
		{pb.Action_ACTION_BAN, false, func(s *stream.TenantSettings) { s.Autoban = false }, true, true},
		{pb.Action_ACTION_KICK, false, func(s *stream.TenantSettings) { s.Autokick = false }, true, true},
		{pb.Action_ACTION_MITIGATE, false, func(s *stream.TenantSettings) { s.Mitigations = false }, false, false},
		{pb.Action_ACTION_MITIGATE, true, func(s *stream.TenantSettings) { s.Mitigations = false }, true, true},
		{pb.Action_ACTION_ALERT, false, func(s *stream.TenantSettings) { *s = stream.TenantSettings{Enforcement: true} }, true, false},
		{pb.Action_ACTION_UNBAN, false, func(s *stream.TenantSettings) { s.Autoban = false }, true, false},
		{pb.Action_ACTION_BAN, false, func(s *stream.TenantSettings) { s.Enforcement = false }, true, true},
		{pb.Action_ACTION_UNMITIGATE, false, func(s *stream.TenantSettings) { s.Enforcement = false }, true, true},
	} {
		st := on
		if tc.settings != nil {
			tc.settings(&st)
		}
		v := &pb.Verdict{Action: tc.action, Shadow: tc.shadow}
		if ok := revalidate(v, st); ok != tc.ok || (ok && v.Shadow != tc.wantS) {
			t.Errorf("%v shadow=%v %+v: ok=%v shadow=%v", tc.action, tc.shadow, st, ok, v.Shadow)
		}
	}
}

func TestPlayerRiskFormula(t *testing.T) {
	p := []byte{1}
	if playerRisk(p, []store.TenantReputation{{TenantID: "a", Weight: 5, Contributions: 3}}) != nil {
		t.Fatal("one network is not enough")
	}
	if playerRisk(p, []store.TenantReputation{{Weight: 1}, {Weight: 0}}) != nil {
		t.Fatal("zero weights do not count")
	}
	r := playerRisk(p, []store.TenantReputation{{Weight: 5, Contributions: 3}, {Weight: 0.5, Contributions: 1}})
	if r == nil || r.NetworksFlagged != 2 || r.ConfirmedBans != 4 || r.Risk <= 0.6 || r.Risk >= 0.7 {
		t.Fatalf("%+v", r) // capped: 1 + 0.5 -> 1 - 0.5^1.5
	}
}
