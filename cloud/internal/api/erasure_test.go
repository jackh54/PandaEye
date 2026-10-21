package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestErasure(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	st := e.d.Store
	// player2 also appears as the target in player1's clip; give player2 a label, a reputation
	// contribution and an inactive (overturned) executed ban of its own.
	check(t, st.InsertLabel(ctx, &store.Label{TenantID: tenantA, PlayerUUID: player2, VerdictID: vMit, Label: store.LabelLegit,
		Source: store.SourceManual, WindowStart: fixedNow.Add(-time.Hour), WindowEnd: fixedNow, CreatedAt: fixedNow}))
	check(t, st.AddReputationContrib(ctx, store.ReputationContrib{TenantID: tenantA, PlayerUUID: player2, VerdictID: vMit, Weight: 1, CreatedAt: fixedNow}))
	old := verdictFixture(tenantA, "0f000000-0000-5000-8000-000000000006", srv2, player2, "Alex", pb.Action_ACTION_BAN, fixedNow.Add(-50*time.Hour))
	_, err := st.UpsertVerdict(ctx, old)
	check(t, err)
	_, err = st.ApplyAck(ctx, store.Ack{TenantID: tenantA, ServerID: srv2, VerdictID: old.ID, Executed: true, At: fixedNow.Add(-50 * time.Hour)})
	check(t, err)
	check(t, st.OverturnBan(ctx, tenantA, old.ID, store.BanOverturn{By: "user:x", At: fixedNow.Add(-49 * time.Hour)}))
	check(t, st.CreateAppeal(ctx, &store.Appeal{TenantID: tenantA, VerdictID: vShadow, PlayerUUID: player2, Message: "m", CreatedAt: fixedNow}, store.DefaultAppealLimits))

	r := e.must(200, "DELETE", "/api/v1/players/"+player2, nil, as(tenantA, "owner"))
	counts := r.json(t)["counts"].(map[string]any)
	want := map[string]float64{"players": 1, "player_names": 1, "verdicts": 3, "verdicts_minimised": 0, "bans": 1, "appeals": 1,
		"labels": 1, "clips": 1, "reputation_contrib": 1}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("counts[%s] = %v, want %v (all: %v)", k, counts[k], n, counts)
		}
	}
	// The tombstone is in PE_ERASURES, analytics were erased, the job is complete and audited.
	u, _ := identity.ParseCanonicalUUID(player2)
	k, _ := stream.ErasureKey(tenantA, u)
	if _, err := e.d.Bus.Erasures.Get(ctx, k); err != nil {
		t.Fatalf("no erasure tombstone: %v", err)
	}
	if calls := e.sink.snapshot(); len(calls) != 1 || calls[0] != tenantA+"/"+player2 {
		t.Fatalf("analytics erasure: %v", e.sink.snapshot())
	}
	job, err := st.GetErasure(ctx, tenantA, player2)
	check(t, err)
	if job.CompletedAt.IsZero() || job.Counts["verdicts"] != 3 {
		t.Fatalf("erasure job: %+v", job)
	}
	if _, err := st.GetClip(ctx, tenantA, clip1); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("clip showing the erased player as a target survived")
	}
	if _, err := st.GetPlayer(ctx, tenantA, player2); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("player row survived")
	}
	// player1's active ban is untouched; player1 keeps their own data apart from the shared clip.
	if b, err := st.GetBan(ctx, tenantA, vBan); err != nil || !b.Active {
		t.Fatalf("other player's ban: %+v %v", b, err)
	}

	// Erasing a banned player keeps a minimal record of the active ban.
	r = e.must(200, "DELETE", "/api/v1/players/"+player1, nil, bearer(mustKey(e, store.ScopeErase)))
	if c := r.json(t)["counts"].(map[string]any); c["verdicts_minimised"] != float64(1) || c["verdicts"] != float64(1) {
		t.Fatalf("erasing a banned player: %v", c)
	}
	v, err := st.GetVerdict(ctx, tenantA, vBan)
	check(t, err)
	if v.PlayerName != "" || v.Reason != "" || len(v.Checks) == 0 || v.PlayerUUID != player1 {
		t.Fatalf("minimal record: %+v", v)
	}

	// A failing analytics store leaves the job incomplete (503) and a retry completes it.
	e.sink.fail(errors.New("clickhouse down"))
	if r := e.do("DELETE", "/api/v1/players/"+player2, nil, as(tenantA, "admin")); r.status != 503 {
		t.Fatalf("analytics failure: %d", r.status)
	}
	if job, _ := st.GetErasure(ctx, tenantA, player2); !job.CompletedAt.IsZero() {
		t.Fatal("erasure marked complete although analytics deletion failed")
	}
	e.sink.fail(nil)
	e.must(200, "DELETE", "/api/v1/players/"+player2, nil, as(tenantA, "admin"))
	if job, _ := st.GetErasure(ctx, tenantA, player2); job.CompletedAt.IsZero() {
		t.Fatal("retry did not complete the erasure")
	}
	if r := e.do("DELETE", "/api/v1/players/not-a-uuid", nil, as(tenantA, "admin")); r.status != 400 {
		t.Fatalf("bad uuid: %d", r.status)
	}
}

func mustKey(e *env, scopes ...string) string {
	_, k := e.apiKey(tenantA, scopes...)
	return k
}

func TestPlayerReputation(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	optIn := true
	for _, id := range []string{tenantA, tenantB, tenantC} {
		_, err := e.d.Store.UpdateTenant(ctx, id, store.TenantPatch{ReputationOptIn: &optIn}, fixedNow)
		check(t, err)
	}
	r := e.must(200, "GET", "/api/v1/players/"+player1, nil, as(tenantA, "viewer"))
	rep, ok := r.json(t)["reputation"].(map[string]any)
	if !ok || rep["risk"] != float64(0) || rep["networks_flagged"] != float64(0) {
		t.Fatalf("no contributions: %v", r.json(t)["reputation"])
	}
	// One other tenant: flagged, but risk stays 0 until two agree.
	check(t, e.d.Store.AddReputationContrib(ctx, store.ReputationContrib{TenantID: tenantB, PlayerUUID: player1, VerdictID: "vb1", Weight: 1, CreatedAt: fixedNow}))
	rep = e.must(200, "GET", "/api/v1/players/"+player1, nil, as(tenantA, "viewer")).json(t)["reputation"].(map[string]any)
	if rep["risk"] != float64(0) || rep["networks_flagged"] != float64(1) {
		t.Fatalf("one tenant: %v", rep)
	}
	check(t, e.d.Store.AddReputationContrib(ctx, store.ReputationContrib{TenantID: tenantC, PlayerUUID: player1, VerdictID: "vc1", Weight: 1, CreatedAt: fixedNow}))
	check(t, e.d.Store.AddReputationContrib(ctx, store.ReputationContrib{TenantID: tenantC, PlayerUUID: player1, VerdictID: "vc2", Weight: 1, CreatedAt: fixedNow}))
	r = e.must(200, "GET", "/api/v1/players/"+player1, nil, as(tenantA, "viewer"))
	rep = r.json(t)["reputation"].(map[string]any)
	keys(t, "reputation", rep, []string{"risk", "networks_flagged", "confirmed_bans"})
	if rep["risk"] != 0.5 || rep["networks_flagged"] != float64(2) || rep["confirmed_bans"] != float64(3) {
		t.Fatalf("two tenants (one capped at weight 1): %v", rep)
	}
	// Not shown when the player did not join this tenant in the last 24 h, or without opt-in.
	later := fixedNow.Add(26 * time.Hour)
	e.setNow(later)
	if _, ok := e.must(200, "GET", "/api/v1/players/"+player1, nil,
		withClaims(testAssertionKey, claimsFor(tenantA, "viewer", "GET", "/api/v1/players/"+player1, later.Add(45*time.Second)))).json(t)["reputation"]; ok {
		t.Fatal("reputation shown for a player who did not join in the last 24 h")
	}
	e.setNow(fixedNow)
	off := false
	_, err := e.d.Store.UpdateTenant(ctx, tenantA, store.TenantPatch{ReputationOptIn: &off}, fixedNow)
	check(t, err)
	if _, ok := e.must(200, "GET", "/api/v1/players/"+player1, nil, as(tenantA, "viewer")).json(t)["reputation"]; ok {
		t.Fatal("reputation shown without opt-in")
	}
}
