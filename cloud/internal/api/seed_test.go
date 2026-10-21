package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// Fixture ids (deterministic so golden files are stable).
const (
	srv1    = "11111111-1111-4111-8111-111111111111"
	srv2    = "22222222-2222-4222-8222-222222222222"
	srvB    = "99999999-9999-4999-8999-999999999999"
	player1 = "aaaaaaaa-0000-4000-8000-000000000001"
	player2 = "aaaaaaaa-0000-4000-8000-000000000002"
	playerB = "bbbbbbbb-0000-4000-8000-000000000001"

	vBan    = "0b000000-0000-5000-8000-000000000001" // executed BAN of player1 (active ban row)
	vAlert  = "0a000000-0000-5000-8000-000000000002" // ALERT of player1
	vShadow = "0c000000-0000-5000-8000-000000000003" // shadow BAN of player2, never executed
	vMit    = "0d000000-0000-5000-8000-000000000004" // MITIGATE of player2, 30 h old
	vB      = "0e000000-0000-5000-8000-000000000005" // tenant B
	clip1   = "c1000000-0000-4000-8000-000000000001"
	clipB   = "c2000000-0000-4000-8000-000000000002"
	appeal1 = "a0000000-0000-4000-8000-000000000001"
	appealB = "a0000000-0000-4000-8000-000000000002"
)

func uuidBytes(s string) []byte {
	u, err := identity.ParseCanonicalUUID(s)
	if err != nil {
		panic(err)
	}
	return u.Bytes()
}

func verdictFixture(tenant, id, server, player, name string, action pb.Action, created time.Time) *store.Verdict {
	pv := &pb.Verdict{VerdictId: id, Player: uuidBytes(player), PlayerName: name, Action: action, CreatedUnixMs: uint64(created.UnixMilli())}
	payload, _ := proto.Marshal(pv)
	return &store.Verdict{ID: id, TenantID: tenant, ServerID: server, NetworkID: "net-a", PlayerUUID: player, PlayerName: name,
		SessionID: "5e550000-0000-4000-8000-000000000001", Action: action, Category: pb.Category_CATEGORY_HEURISTICS,
		Score: 125.5, Confidence: 0.93, Reason: "Combat heuristics (killaura.hitbox x7, reach.distance x3)",
		Checks: []string{"killaura.hitbox", "reach.distance"}, Payload: payload, Status: store.StatusOpen, CreatedAt: created,
		Evidence: json.RawMessage(`{"score":125.5,"confidence":0.93,"leniency":4,"thresholds":{"alert":15,"mitigate":30,"kick":70,"ban":120},` +
			`"families":[{"family":"killaura","contribution":80,"cap":120}],` +
			`"checks":[{"check":"killaura.hitbox","contribution":80,"cap":80,"confidence":0.85,"count":7}],` +
			`"signals":[{"check":"killaura.hitbox","category":"HEURISTICS","points":12,"confidence":0.85,"info":"hitbox miss",` +
			`"data":{"angle":42.5},"time":"` + created.Add(-5*time.Minute).Format(time.RFC3339) + `","clip_id":"","local":false}],` +
			`"session":{"session_id":"5e550000-0000-4000-8000-000000000001","server_id":"` + server + `","started_at":"` +
			created.Add(-time.Hour).Format(time.RFC3339) + `","hot_loaded":false,"exempt":false}}`),
	}
}

// seed fills tenant A (and a little of tenant B) with servers, players, verdicts (an executed BAN
// with its ban row, an ALERT, a shadow BAN, a MITIGATE), a clip, an appeal, usage and probe stats.
func (e *env) seed() {
	t := e.t
	ctx := context.Background()
	st := e.d.Store
	for _, sv := range []*store.Server{
		{TenantID: tenantA, ID: srv1, Name: "Survival", NetworkID: "net-a", Platform: pb.Platform_PLATFORM_PAPER, PlatformVersion: "1.21.11-R0.1",
			PluginVersion: "0.1.0", MinecraftVersion: "1.21.11", SecretHash: identity.HashSecret("s1"), LastSeenAt: fixedNow.Add(-time.Minute), CreatedAt: fixedNow.Add(-48 * time.Hour)},
		{TenantID: tenantA, ID: srv2, Name: "Lobby", NetworkID: "net-a", Platform: pb.Platform_PLATFORM_FOLIA, PlatformVersion: "1.21.11",
			PluginVersion: "0.1.0", MinecraftVersion: "1.21.11", SecretHash: identity.HashSecret("s2"), LastSeenAt: fixedNow.Add(-3 * time.Hour), CreatedAt: fixedNow.Add(-24 * time.Hour)},
		{TenantID: tenantB, ID: srvB, Name: "B-Server", SecretHash: identity.HashSecret("sb"), CreatedAt: fixedNow.Add(-24 * time.Hour)},
	} {
		check(t, st.CreateServer(ctx, sv))
	}
	check(t, st.UpsertPlayer(ctx, tenantA, player1, "Steve_Old", srv2, fixedNow.Add(-48*time.Hour)))
	check(t, st.UpsertPlayer(ctx, tenantA, player1, "Steve", srv1, fixedNow.Add(-time.Hour)))
	check(t, st.UpsertPlayer(ctx, tenantA, player2, "Alex", srv2, fixedNow.Add(-2*time.Hour)))
	check(t, st.UpsertPlayer(ctx, tenantB, playerB, "Bob", srvB, fixedNow.Add(-time.Hour)))

	ban := verdictFixture(tenantA, vBan, srv1, player1, "Steve", pb.Action_ACTION_BAN, fixedNow.Add(-2*time.Hour))
	ban.ClipIDs = []string{clip1}
	ban.AppealCode = identity.AppealCode([]byte(testOutboundKey), vBan)
	alert := verdictFixture(tenantA, vAlert, srv1, player1, "Steve", pb.Action_ACTION_ALERT, fixedNow.Add(-3*time.Hour))
	alert.Score, alert.Confidence = 16, 0.5
	shadow := verdictFixture(tenantA, vShadow, srv2, player2, "Alex", pb.Action_ACTION_BAN, fixedNow.Add(-4*time.Hour))
	shadow.Shadow = true
	shadow.AppealCode = identity.AppealCode([]byte(testOutboundKey), vShadow)
	mit := verdictFixture(tenantA, vMit, srv2, player2, "Alex", pb.Action_ACTION_MITIGATE, fixedNow.Add(-30*time.Hour))
	mit.Category, mit.Mitigations, mit.Strength = pb.Category_CATEGORY_MOVEMENT, []pb.Mitigation{pb.Mitigation_MITIGATION_SETBACK}, 0.5
	vb := verdictFixture(tenantB, vB, srvB, playerB, "Bob", pb.Action_ACTION_BAN, fixedNow.Add(-time.Hour))
	vb.AppealCode = identity.AppealCode([]byte(testOutboundKey), vB)
	for _, v := range []*store.Verdict{ban, alert, shadow, mit, vb} {
		if _, err := st.UpsertVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	check(t, st.MarkDelivered(ctx, tenantA, []string{vBan}, fixedNow.Add(-2*time.Hour+time.Second)))
	for _, a := range []store.Ack{
		{TenantID: tenantA, ServerID: srv1, VerdictID: vBan, Executed: true, Note: "banned", At: fixedNow.Add(-2*time.Hour + 2*time.Second)},
		{TenantID: tenantB, ServerID: srvB, VerdictID: vB, Executed: true, At: fixedNow.Add(-time.Hour)},
	} {
		if _, err := st.ApplyAck(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []*store.Clip{e.clip(tenantA, clip1, srv1, player1, player2), e.clip(tenantB, clipB, srvB, playerB, "")} {
		check(t, st.SaveClip(ctx, c))
	}
	check(t, st.CreateAppeal(ctx, &store.Appeal{ID: appeal1, TenantID: tenantA, VerdictID: vBan, PlayerUUID: player1,
		Message: "I was not cheating", Contact: "steve@example.test", CreatedAt: fixedNow.Add(-time.Hour)}, store.DefaultAppealLimits))
	check(t, st.CreateAppeal(ctx, &store.Appeal{ID: appealB, TenantID: tenantB, VerdictID: vB, PlayerUUID: playerB,
		Message: "B appeal", CreatedAt: fixedNow.Add(-time.Hour)}, store.DefaultAppealLimits))
	check(t, st.RecordPeakPlayers(ctx, tenantA, fixedNow.Add(-24*time.Hour), 12))
	check(t, st.RecordPeakPlayers(ctx, tenantA, fixedNow, 20))
	var h1, h24 store.ProbeHistogram
	for _, us := range []int64{20_000, 40_000, 60_000} {
		h1.Observe(us)
	}
	h24.Observe(150_000)
	check(t, st.RecordProbeStats(ctx, tenantA, srv1, store.ProbeSourcePlugin, fixedNow.Add(-10*time.Minute), h1, 60_000))
	check(t, st.RecordProbeStats(ctx, tenantA, srv1, store.ProbeSourcePlugin, fixedNow.Add(-5*time.Hour), h24, 150_000))
}

// clip builds a stored clip of player with an attack on target (when set).
func (e *env) clip(tenant, id, server, player, target string) *store.Clip {
	start := uint64(fixedNow.Add(-2*time.Hour - 25*time.Second).UnixMicro())
	ev := []*pb.Event{
		{Player: uuidBytes(player), TimeUs: start, Tick: 10, Kind: &pb.Event_Move{Move: &pb.MoveSample{X: 1.5, Y: 64, Z: -3.25, Yaw: 90, Pitch: 10, OnGround: true, HasPos: true, HasRot: true}}},
		{Player: uuidBytes(player), TimeUs: start + 50_000, Tick: 11, Kind: &pb.Event_Swing{Swing: &pb.Swing{}}},
	}
	cd := &pb.ClipData{Player: uuidBytes(player), StartUs: start, EndUs: start + 25_000_000, FormatVersion: 1}
	var subjects []string
	if target != "" {
		ev = append(ev, &pb.Event{Player: uuidBytes(player), TimeUs: start + 100_000, Tick: 12, Kind: &pb.Event_Attack{Attack: &pb.Attack{
			TargetEntityId: 7, TargetPlayer: uuidBytes(target), TargetType: "minecraft:player", Eye: &pb.Vec3{X: 1.5, Y: 65.62, Z: -3.25},
			TargetHistory: []*pb.TimedPos{{TimeUs: start + 90_000, X: 3, Y: 64, Z: -3}}}}})
		subjects = []string{target}
	}
	cd.Events = ev
	raw, err := proto.Marshal(cd)
	check(e.t, err)
	return &store.Clip{ID: id, TenantID: tenant, ServerID: server, PlayerUUID: player, StartUs: int64(start), EndUs: int64(start + 25_000_000),
		Codec: store.ClipCodecZstd, Data: codec.CompressZstd(raw), Subjects: subjects, CreatedAt: fixedNow.Add(-2 * time.Hour)}
}
