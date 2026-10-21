package api

import (
	"context"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestOverturnExecutedBan(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	// The BAN was executed on srv1; the player is now online on srv2 (open session).
	e.connect(tenantA, srv1, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	e.connect(tenantA, srv2, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_FOLIA})
	check(t, e.d.Store.UpsertPlayer(ctx, tenantA, player1, "Steve", srv2, fixedNow.Add(-time.Minute)))
	check(t, e.d.Store.AddReputationContrib(ctx, store.ReputationContrib{TenantID: tenantA, PlayerUUID: player1, VerdictID: vBan, Weight: 1, CreatedAt: fixedNow}))
	ob := e.outbox(tenantA)

	r := e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/overturn", map[string]string{"note": "false positive"}, as(tenantA, "staff"))
	if r.json(t)["status"] != store.StatusOverturned {
		t.Fatalf("status: %s", r.body)
	}
	unbanID := identity.DerivedVerdictID(vBan, int32(pb.Action_ACTION_UNBAN))
	unmitID := identity.DerivedVerdictID(vBan, int32(pb.Action_ACTION_UNMITIGATE))

	// The ban row: inactive, overturned by the principal from authentication, unban pending.
	ban, err := e.d.Store.GetBan(ctx, tenantA, vBan)
	check(t, err)
	if ban.Active || ban.OverturnedBy != "user:staff-user" || ban.OverturnNote != "false positive" || ban.UnbanVerdictID != unbanID || ban.UnbanState != store.UnbanPending {
		t.Fatalf("ban after overturn: %+v", ban)
	}
	// The UNBAN verdict is stored pending, routed to the executing server, superseding the ban.
	unban, err := e.d.Store.GetVerdict(ctx, tenantA, unbanID)
	check(t, err)
	if unban.Action != pb.Action_ACTION_UNBAN || unban.ServerID != srv1 || unban.Supersedes != vBan || unban.Status != store.StatusOpen || len(unban.Payload) == 0 {
		t.Fatalf("unban verdict: %+v", unban)
	}
	pending, err := e.d.Store.PendingVerdicts(ctx, tenantA, store.PendingQuery{ServerID: srv1, Now: fixedNow.Add(48 * time.Hour), DeliveredBefore: fixedNow.Add(48 * time.Hour)})
	check(t, err)
	if len(pending) != 1 || pending[0].ID != unbanID {
		t.Fatalf("UNBAN must stay pending for the executing server: %v", pending)
	}
	unmit, err := e.d.Store.GetVerdict(ctx, tenantA, unmitID)
	check(t, err)
	if unmit.Action != pb.Action_ACTION_UNMITIGATE || unmit.ServerID != srv2 || unmit.Supersedes != vBan {
		t.Fatalf("unmitigate must go to the open session's server: %+v", unmit)
	}
	// Both are published as VerdictRecords for the store writer ...
	recs := e.verdictRecords(tenantA)
	got := map[string]*pb.VerdictRecord{}
	for _, rec := range recs {
		got[rec.Verdict.VerdictId] = rec
	}
	if rec := got[unbanID]; rec == nil || rec.ServerId != srv1 || rec.Verdict.Action != pb.Action_ACTION_UNBAN || rec.Verdict.Supersedes != vBan ||
		!bytesEqual(rec.Verdict.Player, uuidBytes(player1)) {
		t.Fatalf("UNBAN record: %v", rec)
	}
	if rec := got[unmitID]; rec == nil || rec.ServerId != srv2 {
		t.Fatalf("UNMITIGATE record: %v", rec)
	}
	// ... and sent to the connected servers.
	outs := ob.all(500 * time.Millisecond)
	sent := map[string]string{}
	for _, o := range outs {
		if v := o.GetVerdict(); v != nil {
			sent[v.VerdictId] = o.ServerId
		}
	}
	if sent[unbanID] != srv1 || sent[unmitID] != srv2 {
		t.Fatalf("outbound verdicts: %v", sent)
	}
	// Label legit, reputation removed.
	cover, err := e.d.Store.HasLegitCover(ctx, tenantA, player1, fixedNow.Add(-2*time.Hour-time.Minute), fixedNow.Add(-2*time.Hour))
	check(t, err)
	if !cover {
		t.Fatal("no legit label over the signal window")
	}
	if rows, _ := e.d.Store.PlayerReputation(ctx, tenantB, player1); len(rows) != 0 {
		t.Fatalf("reputation contribution kept: %v", rows)
	}
	// The dashboard sees "unban pending" until the executing server acks.
	b := e.must(200, "GET", "/api/v1/bans", nil, as(tenantA, "viewer")).json(t)["bans"].([]any)[0].(map[string]any)
	if b["unban_state"] != "pending" || b["active"] != false || b["unban_verdict_id"] != unbanID {
		t.Fatalf("ban json: %v", b)
	}
	if _, err := e.d.Store.ApplyAck(ctx, store.Ack{TenantID: tenantA, ServerID: srv1, VerdictID: unbanID, Executed: true}); err != nil {
		t.Fatal(err)
	}
	b = e.must(200, "GET", "/api/v1/bans", nil, as(tenantA, "viewer")).json(t)["bans"].([]any)[0].(map[string]any)
	if b["unban_state"] != "done" {
		t.Fatalf("after the ack: %v", b)
	}

	// A retry is idempotent: same ids, nothing duplicated, the acked UNBAN is not re-sent.
	e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/overturn", map[string]string{}, as(tenantA, "staff"))
	vs, _, err := e.d.Store.ListVerdicts(ctx, tenantA, store.VerdictFilter{PlayerUUID: player1}, store.Page{Limit: 200})
	check(t, err)
	if len(vs) != 4 { // ban, alert, unban, unmitigate
		t.Fatalf("verdicts after a retried overturn: %d", len(vs))
	}
	for _, o := range ob.all(300 * time.Millisecond) {
		if o.GetVerdict().GetVerdictId() == unbanID {
			t.Fatal("acked UNBAN re-sent")
		}
	}
	if n := countLabels(t, e, player1); n != 1 {
		t.Fatalf("labels after retry: %d", n)
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

func countLabels(t *testing.T, e *env, player string) int {
	t.Helper()
	ls, _, err := e.d.Store.ExportLabels(context.Background(), tenantA, time.Time{}, store.Page{Limit: 200})
	check(t, err)
	n := 0
	for _, l := range ls {
		if l.PlayerUUID == player {
			n++
		}
	}
	return n
}

func TestOverturnNotExecutedBan(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ob := e.outbox(tenantA)
	e.must(200, "POST", "/api/v1/verdicts/"+vShadow+"/overturn", nil, as(tenantA, "staff"))
	unbanID := identity.DerivedVerdictID(vShadow, int32(pb.Action_ACTION_UNBAN))
	if _, err := e.d.Store.GetVerdict(ctx, tenantA, unbanID); err == nil {
		t.Fatal("UNBAN created for a ban that was never executed")
	}
	unmit, err := e.d.Store.GetVerdict(ctx, tenantA, identity.DerivedVerdictID(vShadow, int32(pb.Action_ACTION_UNMITIGATE)))
	check(t, err)
	if unmit.ServerID != srv2 {
		t.Fatalf("UNMITIGATE routed to %s", unmit.ServerID)
	}
	// srv2 is not connected: nothing is sent now (the gateway delivers the pending UNMITIGATE on reconnect).
	if o := ob.next(300 * time.Millisecond); o != nil {
		t.Fatalf("sent to a disconnected server: %v", o)
	}
	recs := e.verdictRecords(tenantA)
	if len(recs) != 1 || recs[0].Verdict.Action != pb.Action_ACTION_UNMITIGATE {
		t.Fatalf("records: %v", recs)
	}
	// Corrective verdicts themselves cannot be confirmed or overturned.
	if r := e.do("POST", "/api/v1/verdicts/"+unmit.ID+"/overturn", nil, as(tenantA, "staff")); r.status != 409 {
		t.Fatalf("overturning an UNMITIGATE: %d", r.status)
	}
}

func TestConfirmThenOverturn(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	// Tenant A becomes trusted and opted in so the executed BAN with a cloud check is eligible.
	check(t, e.d.Store.UpsertPlayer(ctx, tenantA, player1, "Steve", srv1, fixedNow))
	plan := "pro"
	optIn := true
	_, err := e.d.Store.UpdateTenant(ctx, tenantA, store.TenantPatch{Plan: &plan, ReputationOptIn: &optIn}, fixedNow)
	check(t, err)
	r := e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/confirm", map[string]string{"note": "clear"}, as(tenantA, "staff"))
	if r.json(t)["status"] != store.StatusConfirmed {
		t.Fatalf("confirm: %s", r.body)
	}
	if rows, _ := e.d.Store.PlayerReputation(ctx, tenantB, player1); len(rows) != 1 {
		t.Fatalf("no reputation contribution after confirming an eligible ban: %v", rows)
	}
	ls, _, _ := e.d.Store.ExportLabels(ctx, tenantA, time.Time{}, store.Page{})
	if len(ls) != 1 || ls[0].Label.Label != store.LabelCheat || ls[0].Source != store.SourceConfirmed {
		t.Fatalf("labels after confirm: %+v", ls)
	}
	// Confirming the ALERT (no executed ban) labels it but contributes nothing.
	e.must(200, "POST", "/api/v1/verdicts/"+vAlert+"/confirm", nil, as(tenantA, "staff"))
	if rows, _ := e.d.Store.PlayerReputation(ctx, tenantB, player1); len(rows) != 1 || rows[0].Contributions != 1 {
		t.Fatalf("reputation after confirming an alert: %v", rows)
	}

	e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/overturn", nil, as(tenantA, "staff"))
	if rows, _ := e.d.Store.PlayerReputation(ctx, tenantB, player1); len(rows) != 0 {
		t.Fatalf("reputation after overturn: %v", rows)
	}
	ls, _, _ = e.d.Store.ExportLabels(ctx, tenantA, time.Time{}, store.Page{})
	labels := map[string]string{}
	for _, l := range ls {
		labels[l.VerdictID+"/"+l.Label.Label] = l.Source
	}
	if _, ok := labels[vBan+"/cheat"]; ok || labels[vBan+"/legit"] != store.SourceOverturned || labels[vAlert+"/cheat"] != store.SourceConfirmed {
		t.Fatalf("labels after overturn: %v", labels)
	}
	if r := e.do("POST", "/api/v1/verdicts/"+vBan+"/confirm", nil, as(tenantA, "staff")); r.status != 409 {
		t.Fatalf("confirming an overturned verdict: %d", r.status)
	}
}

func TestAppealAcceptOverturns(t *testing.T) {
	e := newEnv(t)
	e.seed()
	r := e.must(200, "POST", "/api/v1/appeals/"+appeal1+"/decide", map[string]string{"decision": "accept", "note": "sorry"}, as(tenantA, "staff"))
	if a := r.json(t); a["status"] != store.AppealAccepted || a["decided_by"] != "user:staff-user" {
		t.Fatalf("appeal: %v", a)
	}
	v, err := e.d.Store.GetVerdict(context.Background(), tenantA, vBan)
	check(t, err)
	if v.Status != store.StatusOverturned {
		t.Fatalf("accepting the appeal must overturn: %s", v.Status)
	}
	if b, _ := e.d.Store.GetBan(context.Background(), tenantA, vBan); b.Active || b.UnbanState != store.UnbanPending {
		t.Fatalf("ban: %+v", b)
	}
	if r := e.do("POST", "/api/v1/appeals/"+appeal1+"/decide", map[string]string{"decision": "maybe"}, as(tenantA, "staff")); r.status != 400 {
		t.Fatalf("bad decision: %d", r.status)
	}
}
