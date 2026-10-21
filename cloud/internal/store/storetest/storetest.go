// Package storetest is the conformance suite every store.Store implementation must pass. sqlstore runs
// it against sqlite always and against postgres when PE_TEST_POSTGRES_DSN is set.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Opener returns a fresh, migrated, empty store for one subtest (closed by the opener's cleanup).
type Opener func(t *testing.T) store.Store

// Run runs the whole suite; every subtest gets its own store.
func Run(t *testing.T, open Opener) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"Tenants", testTenants},
		{"Licenses", testLicenses},
		{"Servers", testServers},
		{"Players", testPlayers},
		{"Verdicts", testVerdicts},
		{"PendingVerdicts", testPendingVerdicts},
		{"AcksAndBans", testAcksAndBans},
		{"Appeals", testAppeals},
		{"Labels", testLabels},
		{"Clips", testClips},
		{"APIKeys", testAPIKeys},
		{"Reputation", testReputation},
		{"Usage", testUsage},
		{"ProbeStats", testProbeStats},
		{"Audit", testAudit},
		{"Erasure", testErasure},
		{"Meta", testMeta},
		{"Retention", testRetention},
		{"ConcurrentWrites", testConcurrentWrites},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, open(t)) })
	}
}

var (
	ctx = context.Background()
	t0  = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got error %v, want %v", err, want)
	}
}

func newTenant(t *testing.T, s store.Store, name string) string {
	t.Helper()
	id := identity.NewTenantID()
	check(t, s.CreateTenant(ctx, store.NewTenant(id, name, identity.Slug(name)+"-"+id[2:8], t0)))
	return id
}

func uuid() string { return identity.NewUUID().String() }

func verdict(tenant, server, player string, action pb.Action, created time.Time) *store.Verdict {
	return &store.Verdict{
		ID: uuid(), TenantID: tenant, ServerID: server, PlayerUUID: player, PlayerName: "Steve", SessionID: "s1",
		Action: action, Category: pb.Category_CATEGORY_HEURISTICS, Score: 130, Confidence: 0.93, Reason: "Combat heuristics",
		Checks: []string{"killaura.hitbox", "aim.snap"}, Evidence: json.RawMessage(`{"score":130}`),
		Mitigations: []pb.Mitigation{pb.Mitigation_MITIGATION_HIT_CANCEL}, Strength: 0.5, Payload: []byte{1, 2, 3},
		AppealCode: "ABCDEFGHJK", CreatedAt: created,
	}
}

func testTenants(t *testing.T, s store.Store) {
	a := newTenant(t, s, "Network A")
	got, err := s.GetTenant(ctx, a)
	check(t, err)
	if got.Leniency != 4 || !got.Mitigations || got.Autoban || got.Plan != store.PlanFree || got.DiscordMinAction != pb.Action_ACTION_MITIGATE || !got.CreatedAt.Equal(t0) {
		t.Fatalf("defaults: %+v", got)
	}
	dup := store.NewTenant(identity.NewTenantID(), "Other", got.Slug, t0)
	wantErr(t, s.CreateTenant(ctx, dup), store.ErrConflict)
	_, err = s.GetTenant(ctx, identity.NewTenantID())
	wantErr(t, err, store.ErrNotFound)

	lvl, yes, hook, plan := 2, true, []byte{9, 9}, "pro"
	up, err := s.UpdateTenant(ctx, a, store.TenantPatch{Leniency: &lvl, Enforcement: &yes, DiscordWebhookEnc: &hook, Plan: &plan}, t0.Add(time.Hour))
	check(t, err)
	if up.Leniency != 2 || !up.Enforcement || string(up.DiscordWebhookEnc) != string(hook) || up.Plan != "pro" || !up.UpdatedAt.Equal(t0.Add(time.Hour)) || up.Autoban {
		t.Fatalf("patch: %+v", up)
	}
	var none []byte
	up, err = s.UpdateTenant(ctx, a, store.TenantPatch{DiscordWebhookEnc: &none}, t0)
	check(t, err)
	if up.DiscordWebhookEnc != nil {
		t.Fatal("webhook not cleared")
	}
	_, err = s.UpdateTenant(ctx, identity.NewTenantID(), store.TenantPatch{Leniency: &lvl}, t0)
	wantErr(t, err, store.ErrNotFound)

	check(t, s.RecordLicenseCheck(ctx, a, 5, t0, t0.Add(24*time.Hour)))
	check(t, s.SetOverLimitSince(ctx, a, t0))
	got, _ = s.GetTenant(ctx, a)
	if got.MaxServers != 5 || !got.LicenseVerifiedAt.Equal(t0) || !got.LicenseValidUntil.Equal(t0.Add(24*time.Hour)) || !got.OverLimitSince.Equal(t0) {
		t.Fatalf("licence check: %+v", got)
	}
	check(t, s.SetOverLimitSince(ctx, a, time.Time{}))
	got, _ = s.GetTenant(ctx, a)
	if !got.OverLimitSince.IsZero() {
		t.Fatal("over-limit not cleared")
	}
	wantErr(t, s.RecordLicenseCheck(ctx, identity.NewTenantID(), 1, t0, t0), store.ErrNotFound)

	b := newTenant(t, s, "Network B")
	ids, err := s.ListTenantIDs(ctx)
	check(t, err)
	if len(ids) != 2 {
		t.Fatalf("tenant ids %v", ids)
	}

	check(t, s.ReplaceTenantChecks(ctx, a, []store.TenantCheck{
		{CheckID: "reach.distance", Enabled: true, Overrides: map[string]float64{"limit": 3.1}},
		{CheckID: "autoclicker.cps", Enabled: true},
	}))
	check(t, s.ReplaceTenantChecks(ctx, b, []store.TenantCheck{{CheckID: "aim.gcd", Enabled: false}}))
	cs, err := s.ListTenantChecks(ctx, a)
	check(t, err)
	if len(cs) != 2 || cs[0].CheckID != "autoclicker.cps" || cs[1].Overrides["limit"] != 3.1 || !cs[1].Enabled {
		t.Fatalf("checks %+v", cs)
	}
	check(t, s.ReplaceTenantChecks(ctx, a, []store.TenantCheck{{CheckID: "aim.snap", Enabled: false}}))
	cs, _ = s.ListTenantChecks(ctx, a)
	if len(cs) != 1 || cs[0].CheckID != "aim.snap" || cs[0].Enabled {
		t.Fatalf("replace %+v", cs)
	}
	if cs, _ := s.ListTenantChecks(ctx, b); len(cs) != 1 || cs[0].CheckID != "aim.gcd" {
		t.Fatalf("other tenant touched: %+v", cs)
	}
}

func testLicenses(t *testing.T, s store.Store) {
	key := identity.LicenseKeyHMAC([]byte("pepper-pepper-pepper-pepper-pepp"), "KEY-1")
	nt := store.NewTenant(identity.NewTenantID(), "First", "first", t0)
	ten, lic, created, err := s.EnsureLicenseTenant(ctx, key, nt, t0)
	check(t, err)
	if !created || ten.ID != nt.ID || lic.TenantID != nt.ID {
		t.Fatalf("first use: %v %+v", created, ten)
	}
	ten2, _, created, err := s.EnsureLicenseTenant(ctx, key, store.NewTenant(identity.NewTenantID(), "Second", "second", t0), t0)
	check(t, err)
	if created || ten2.ID != nt.ID {
		t.Fatal("second use must map to the first tenant")
	}
	if ids, _ := s.ListTenantIDs(ctx); len(ids) != 1 {
		t.Fatalf("second tenant created: %v", ids)
	}
	_, _, _, err = s.EnsureLicenseTenant(ctx, identity.LicenseKeyHMAC([]byte("p"), "KEY-2"), store.NewTenant(identity.NewTenantID(), "Dup", "first", t0), t0)
	wantErr(t, err, store.ErrConflict)

	l, err := s.LookupLicense(ctx, key)
	check(t, err)
	if l.TenantID != nt.ID || !l.RevokedAt.IsZero() {
		t.Fatalf("lookup %+v", l)
	}
	key2 := identity.LicenseKeyHMAC([]byte("p"), "KEY-3")
	check(t, s.CreateLicense(ctx, nt.ID, key2, t0.Add(time.Minute)))
	wantErr(t, s.CreateLicense(ctx, nt.ID, key2, t0), store.ErrConflict)
	other := newTenant(t, s, "Other")
	wantErr(t, s.RevokeLicense(ctx, other, key, t0), store.ErrNotFound)
	check(t, s.RevokeLicense(ctx, nt.ID, key, t0.Add(time.Hour)))
	ls, err := s.ListLicenses(ctx, nt.ID)
	check(t, err)
	if len(ls) != 2 || string(ls[0].KeyHMAC) != string(key2) || ls[1].RevokedAt.IsZero() {
		t.Fatalf("licences %+v", ls)
	}
	_, l, created, err = s.EnsureLicenseTenant(ctx, key, store.NewTenant(identity.NewTenantID(), "X", "x", t0), t0)
	check(t, err)
	if created || l.RevokedAt.IsZero() {
		t.Fatal("revoked key must map to its tenant and report revocation")
	}
	_, err = s.LookupLicense(ctx, []byte("missing"))
	wantErr(t, err, store.ErrNotFound)
}

func testServers(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	sid := uuid()
	srv := &store.Server{TenantID: a, ID: sid, Name: "SMP", Platform: pb.Platform_PLATFORM_PAPER, SecretHash: identity.HashSecret("s"), CreatedAt: t0}
	check(t, s.CreateServer(ctx, srv))
	wantErr(t, s.CreateServer(ctx, srv), store.ErrConflict)
	// The same server id in another tenant is a different server.
	check(t, s.CreateServer(ctx, &store.Server{TenantID: b, ID: sid, Name: "Other", SecretHash: identity.HashSecret("t"), CreatedAt: t0}))
	wantErr(t, s.CreateServer(ctx, &store.Server{TenantID: identity.NewTenantID(), ID: uuid(), SecretHash: []byte{1}}), store.ErrNotFound)

	got, err := s.GetServer(ctx, a, sid)
	check(t, err)
	if got.Name != "SMP" || got.Platform != pb.Platform_PLATFORM_PAPER || !identity.CheckSecret(got.SecretHash, "s") {
		t.Fatalf("server %+v", got)
	}
	if g, _ := s.GetServer(ctx, b, sid); g.Name != "Other" {
		t.Fatal("tenant isolation")
	}
	_, err = s.GetServer(ctx, a, uuid())
	wantErr(t, err, store.ErrNotFound)

	srv.Name, srv.PluginVersion, srv.InstanceID, srv.LastSeenAt = "SMP-2", "1.2.3", "inst", t0.Add(time.Minute)
	check(t, s.UpdateServerSeen(ctx, srv))
	check(t, s.AdvanceServerSeq(ctx, a, sid, 500, t0.Add(2*time.Minute)))
	check(t, s.AdvanceServerSeq(ctx, a, sid, 300, t0.Add(3*time.Minute)))
	got, _ = s.GetServer(ctx, a, sid)
	if got.Name != "SMP-2" || got.PluginVersion != "1.2.3" || got.InstanceID != "inst" || got.LastBatchSeq != 500 || !got.LastSeenAt.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("updated %+v", got)
	}
	if g, _ := s.GetServer(ctx, b, sid); g.LastBatchSeq != 0 {
		t.Fatal("seq leaked across tenants")
	}
	wantErr(t, s.AdvanceServerSeq(ctx, a, uuid(), 1, t0), store.ErrNotFound)

	for i := 0; i < 4; i++ {
		check(t, s.CreateServer(ctx, &store.Server{TenantID: a, ID: uuid(), SecretHash: []byte{1}, CreatedAt: t0.Add(time.Duration(i+1) * time.Second)}))
	}
	var seen []string
	page := store.Page{Limit: 2}
	for {
		list, next, err := s.ListServers(ctx, a, page)
		check(t, err)
		for _, v := range list {
			seen = append(seen, v.ID)
		}
		if next == "" {
			break
		}
		page.Cursor = next
	}
	if len(seen) != 5 || seen[4] != sid {
		t.Fatalf("pagination: %v", seen)
	}
	_, _, err = s.ListServers(ctx, a, store.Page{Cursor: "!!"})
	wantErr(t, err, store.ErrBadCursor)
}

func testPlayers(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	p := uuid()
	check(t, s.UpsertPlayer(ctx, a, p, "Old_Name", "srv1", t0))
	check(t, s.UpsertPlayer(ctx, a, p, "New_Name", "srv2", t0.Add(time.Hour)))
	check(t, s.UpsertPlayer(ctx, a, p, "Old_Name", "srv3", t0.Add(30*time.Minute))) // late, older event
	got, err := s.GetPlayer(ctx, a, p)
	check(t, err)
	if got.LastName != "New_Name" || got.LastServerID != "srv2" || !got.FirstSeenAt.Equal(t0) || !got.LastSeenAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("player %+v", got)
	}
	if len(got.Names) != 2 || got.Names[0].Name != "New_Name" {
		t.Fatalf("names %+v", got.Names)
	}
	_, err = s.GetPlayer(ctx, b, p)
	wantErr(t, err, store.ErrNotFound)
	check(t, s.UpsertPlayer(ctx, a, uuid(), "OldXName", "srv1", t0))
	found, err := s.FindPlayersByName(ctx, a, "old_", 10)
	check(t, err)
	if len(found) != 1 || found[0].UUID != p {
		t.Fatalf("underscore must not be a wildcard: %+v", found)
	}
	found, _ = s.FindPlayersByName(ctx, a, "OLD", 10)
	if len(found) != 2 {
		t.Fatalf("case-insensitive prefix: %+v", found)
	}
	if found, _ := s.FindPlayersByName(ctx, b, "old", 10); len(found) != 0 {
		t.Fatal("tenant isolation")
	}
}

func testVerdicts(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	srv, player := uuid(), uuid()
	v := verdict(a, srv, player, pb.Action_ACTION_BAN, t0)
	v.ClipIDs = []string{"c1"}
	v.BanDurationS = 3600
	created, err := s.UpsertVerdict(ctx, v)
	check(t, err)
	if !created {
		t.Fatal("not created")
	}
	again := *v
	again.Score = 1
	if created, err := s.UpsertVerdict(ctx, &again); err != nil || created {
		t.Fatalf("re-upsert must be a no-op: %v %v", created, err)
	}
	got, err := s.GetVerdict(ctx, a, v.ID)
	check(t, err)
	if got.Score != 130 || got.Status != store.StatusOpen || len(got.Checks) != 2 || got.ClipIDs[0] != "c1" || got.Mitigations[0] != pb.Mitigation_MITIGATION_HIT_CANCEL ||
		got.Strength != 0.5 || got.BanDurationS != 3600 || string(got.Payload) != "\x01\x02\x03" || got.AppealCode != "ABCDEFGHJK" || !got.CreatedAt.Equal(t0) {
		t.Fatalf("verdict %+v", got)
	}
	var ev map[string]any
	if err := json.Unmarshal(got.Evidence, &ev); err != nil || ev["score"] != 130.0 {
		t.Fatalf("evidence %s", got.Evidence)
	}
	_, err = s.GetVerdict(ctx, b, v.ID)
	wantErr(t, err, store.ErrNotFound)

	for i := 0; i < 5; i++ {
		x := verdict(a, srv, player, pb.Action_ACTION_ALERT, t0.Add(time.Duration(i+1)*time.Minute))
		x.SessionID = "s2"
		_, err := s.UpsertVerdict(ctx, x)
		check(t, err)
	}
	_, err = s.UpsertVerdict(ctx, verdict(b, srv, player, pb.Action_ACTION_ALERT, t0))
	check(t, err)
	all, next, err := s.ListVerdicts(ctx, a, store.VerdictFilter{Action: pb.Action_ACTION_ALERT}, store.Page{Limit: 3})
	check(t, err)
	if len(all) != 3 || next == "" || !all[0].CreatedAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("list page 1: %d %q", len(all), next)
	}
	rest, next, err := s.ListVerdicts(ctx, a, store.VerdictFilter{Action: pb.Action_ACTION_ALERT}, store.Page{Limit: 3, Cursor: next})
	check(t, err)
	if len(rest) != 2 || next != "" {
		t.Fatalf("list page 2: %d %q", len(rest), next)
	}
	win, _, _ := s.ListVerdicts(ctx, a, store.VerdictFilter{Since: t0.Add(2 * time.Minute), Until: t0.Add(4 * time.Minute), PlayerUUID: player, ServerID: srv}, store.Page{})
	if len(win) != 2 {
		t.Fatalf("time filter: %d", len(win))
	}
	sess, err := s.ListVerdictsBySession(ctx, a, "s2")
	check(t, err)
	if len(sess) != 5 || !sess[0].CreatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("by session %d", len(sess))
	}

	wantErr(t, s.SetVerdictStatus(ctx, a, v.ID, store.StatusConfirmed, store.StatusOverturned), store.ErrConflict)
	check(t, s.SetVerdictStatus(ctx, a, v.ID, store.StatusConfirmed, store.StatusOpen))
	wantErr(t, s.SetVerdictStatus(ctx, b, v.ID, store.StatusOverturned), store.ErrNotFound)
	if open, _, _ := s.ListVerdicts(ctx, a, store.VerdictFilter{Status: store.StatusConfirmed}, store.Page{}); len(open) != 1 {
		t.Fatal("status filter")
	}

	counts, err := s.VerdictCounts(ctx, a, t0)
	check(t, err)
	if len(counts) != 2 || counts[0].Action != pb.Action_ACTION_ALERT || counts[0].Count != 5 || counts[1].Count != 1 {
		t.Fatalf("counts %+v", counts)
	}
}

func testPendingVerdicts(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	srv, other, player := uuid(), uuid(), uuid()
	now := t0.Add(48 * time.Hour)
	mk := func(action pb.Action, age time.Duration, mut func(v *store.Verdict)) string {
		v := verdict(a, srv, player, action, now.Add(-age))
		if mut != nil {
			mut(v)
		}
		_, err := s.UpsertVerdict(ctx, v)
		check(t, err)
		return v.ID
	}
	want := []string{
		mk(pb.Action_ACTION_UNBAN, 40*time.Hour, nil),      // never expires
		mk(pb.Action_ACTION_UNMITIGATE, 30*time.Hour, nil), // never expires
		mk(pb.Action_ACTION_BAN, 23*time.Hour, nil),
		mk(pb.Action_ACTION_MITIGATE, time.Hour, func(v *store.Verdict) { v.DeliveredAt = now.Add(-20 * time.Second) }),
		mk(pb.Action_ACTION_KICK, 4*time.Minute, nil),
	}
	mk(pb.Action_ACTION_BAN, 25*time.Hour, nil)                                                                   // too old
	mk(pb.Action_ACTION_KICK, 6*time.Minute, nil)                                                                 // kick too old
	mk(pb.Action_ACTION_ALERT, time.Minute, func(v *store.Verdict) { v.DeliveredAt = now.Add(-5 * time.Second) }) // delivered recently
	mk(pb.Action_ACTION_ALERT, time.Minute, func(v *store.Verdict) { v.Status = store.StatusExpired })
	mk(pb.Action_ACTION_ALERT, time.Minute, func(v *store.Verdict) { v.ServerID = other })
	acked := mk(pb.Action_ACTION_ALERT, time.Minute, nil)
	_, err := s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: srv, VerdictID: acked, At: now})
	check(t, err)
	_, err = s.UpsertVerdict(ctx, verdict(b, srv, player, pb.Action_ACTION_ALERT, now.Add(-time.Minute)))
	check(t, err)

	got, err := s.PendingVerdicts(ctx, a, store.PendingQuery{ServerID: srv, Now: now, DeliveredBefore: now.Add(-store.PendingResend)})
	check(t, err)
	if len(got) != len(want) {
		t.Fatalf("pending: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("pending[%d] = %s (%v), want %s (oldest first)", i, got[i].ID, got[i].Action, want[i])
		}
	}
	// A fresh connection gets recently delivered ones too.
	fresh, _ := s.PendingVerdicts(ctx, a, store.PendingQuery{ServerID: srv, Now: now, DeliveredBefore: now})
	if len(fresh) != len(want)+1 {
		t.Fatalf("fresh connection: %d", len(fresh))
	}
	if lim, _ := s.PendingVerdicts(ctx, a, store.PendingQuery{ServerID: srv, Now: now, DeliveredBefore: now, Limit: 2}); len(lim) != 2 {
		t.Fatal("limit")
	}
	check(t, s.MarkDelivered(ctx, a, []string{want[0], want[2]}, now))
	check(t, s.MarkDelivered(ctx, b, []string{want[1]}, now)) // other tenant: no effect
	got, _ = s.PendingVerdicts(ctx, a, store.PendingQuery{ServerID: srv, Now: now, DeliveredBefore: now.Add(-store.PendingResend)})
	if len(got) != 3 {
		t.Fatalf("after delivery: %d", len(got))
	}
	if v, _ := s.GetVerdict(ctx, a, want[0]); !v.DeliveredAt.Equal(now) {
		t.Fatal("delivered_at")
	}
}

func testAcksAndBans(t *testing.T, s store.Store) {
	a := newTenant(t, s, "A")
	srv, exec, player := uuid(), uuid(), uuid()
	ban := verdict(a, srv, player, pb.Action_ACTION_BAN, t0)
	shadowBan := verdict(a, srv, player, pb.Action_ACTION_BAN, t0)
	shadowBan.Shadow = true
	kick := verdict(a, srv, player, pb.Action_ACTION_KICK, t0)
	for _, v := range []*store.Verdict{ban, shadowBan, kick} {
		_, err := s.UpsertVerdict(ctx, v)
		check(t, err)
	}
	r, err := s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: ban.ID, Executed: false, Note: "offline", At: t0.Add(time.Second)})
	check(t, err)
	if r.BanCreated || r.Verdict.Executed || r.Verdict.AckNote != "offline" {
		t.Fatalf("non-executed ack: %+v", r)
	}
	r, err = s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: ban.ID, Executed: true, At: t0.Add(time.Minute)})
	check(t, err)
	if !r.BanCreated || !r.Verdict.Executed || r.Verdict.AckNote != "offline" || !r.Verdict.AckedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("executed ack: %+v %+v", r, r.Verdict)
	}
	r, _ = s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: ban.ID, Executed: false, At: t0.Add(time.Hour)})
	if r.BanCreated || !r.Verdict.Executed {
		t.Fatal("executed must be sticky and the ban created once")
	}
	if r, _ := s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: shadowBan.ID, Executed: true}); r.BanCreated {
		t.Fatal("shadow BAN created a ban row")
	}
	if r, _ := s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: kick.ID, Executed: true}); r.BanCreated {
		t.Fatal("KICK created a ban row")
	}
	_, err = s.ApplyAck(ctx, store.Ack{TenantID: newTenant(t, s, "B"), VerdictID: ban.ID, Executed: true})
	wantErr(t, err, store.ErrNotFound)

	b, err := s.GetBan(ctx, a, ban.ID)
	check(t, err)
	if !b.Active || b.ServerID != exec || b.PlayerUUID != player || b.UnbanState != store.UnbanNone || b.Reason != ban.Reason {
		t.Fatalf("ban %+v", b)
	}
	_, err = s.GetBan(ctx, a, shadowBan.ID)
	wantErr(t, err, store.ErrNotFound)
	yes := true
	if list, _, _ := s.ListBans(ctx, a, store.BanFilter{Active: &yes, PlayerUUID: player}, store.Page{}); len(list) != 1 {
		t.Fatal("list active bans")
	}

	unban := verdict(a, exec, player, pb.Action_ACTION_UNBAN, t0.Add(2*time.Hour))
	unban.Supersedes = ban.ID
	_, err = s.UpsertVerdict(ctx, unban)
	check(t, err)
	check(t, s.OverturnBan(ctx, a, ban.ID, store.BanOverturn{By: "user:alice", Note: "false positive", UnbanVerdictID: unban.ID, At: t0.Add(2 * time.Hour)}))
	wantErr(t, s.OverturnBan(ctx, a, ban.ID, store.BanOverturn{By: "user:bob"}), store.ErrConflict)
	b, _ = s.GetBan(ctx, a, ban.ID)
	if b.Active || b.UnbanState != store.UnbanPending || b.OverturnedBy != "user:alice" || b.UnbanVerdictID != unban.ID {
		t.Fatalf("overturned %+v", b)
	}
	no := false
	if list, _, _ := s.ListBans(ctx, a, store.BanFilter{Active: &no}, store.Page{}); len(list) != 1 {
		t.Fatal("list inactive")
	}
	r, err = s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: exec, VerdictID: unban.ID, Executed: true})
	check(t, err)
	if !r.UnbanDone {
		t.Fatal("unban not completed")
	}
	if b, _ := s.GetBan(ctx, a, ban.ID); b.UnbanState != store.UnbanDone {
		t.Fatal("unban state")
	}
}

func testAppeals(t *testing.T, s store.Store) {
	a := newTenant(t, s, "A")
	player := uuid()
	v := verdict(a, uuid(), player, pb.Action_ACTION_BAN, t0)
	_, err := s.UpsertVerdict(ctx, v)
	check(t, err)
	ap := &store.Appeal{TenantID: a, VerdictID: v.ID, PlayerUUID: player, Message: "I was lagging", Contact: "me@example.org", CreatedAt: t0}
	check(t, s.CreateAppeal(ctx, ap, store.DefaultAppealLimits))
	if ap.ID == "" || ap.Status != store.AppealOpen {
		t.Fatalf("appeal %+v", ap)
	}
	wantErr(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: v.ID, PlayerUUID: player, Message: "again", CreatedAt: t0}, store.DefaultAppealLimits), store.ErrLimitExceeded)
	got, err := s.DecideAppeal(ctx, a, ap.ID, store.AppealDenied, "user:alice", "no", t0.Add(time.Hour))
	check(t, err)
	if got.Status != store.AppealDenied || got.DecidedBy != "user:alice" || !got.DecidedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("decided %+v", got)
	}
	_, err = s.DecideAppeal(ctx, a, ap.ID, store.AppealAccepted, "user:bob", "", t0)
	wantErr(t, err, store.ErrConflict)
	_, err = s.DecideAppeal(ctx, a, uuid(), store.AppealAccepted, "user:bob", "", t0)
	wantErr(t, err, store.ErrNotFound)
	for i := 0; i < 2; i++ {
		x := &store.Appeal{TenantID: a, VerdictID: v.ID, PlayerUUID: player, Message: "m", CreatedAt: t0.Add(time.Duration(i+1) * time.Minute)}
		check(t, s.CreateAppeal(ctx, x, store.DefaultAppealLimits))
		_, err := s.DecideAppeal(ctx, a, x.ID, store.AppealDenied, "user:alice", "", t0.Add(2*time.Hour))
		check(t, err)
	}
	wantErr(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: v.ID, PlayerUUID: player, Message: "4th"}, store.DefaultAppealLimits), store.ErrLimitExceeded)
	v2 := verdict(a, uuid(), player, pb.Action_ACTION_BAN, t0)
	_, _ = s.UpsertVerdict(ctx, v2)
	tight := store.AppealLimits{OpenPerVerdict: 1, TotalPerVerdict: 3, PerTenantPerDay: 3}
	wantErr(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: v2.ID, PlayerUUID: player, Message: "m", CreatedAt: t0.Add(time.Hour)}, tight), store.ErrLimitExceeded)
	check(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: v2.ID, PlayerUUID: player, Message: "m", CreatedAt: t0.Add(25 * time.Hour)}, tight))
	wantErr(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: newTenant(t, s, "B"), VerdictID: v.ID, Message: "x"}, store.DefaultAppealLimits), store.ErrNotFound)

	list, _, err := s.ListAppeals(ctx, a, store.AppealFilter{VerdictID: v.ID}, store.Page{})
	check(t, err)
	if len(list) != 3 {
		t.Fatalf("appeals for verdict: %d", len(list))
	}
	if open, _, _ := s.ListAppeals(ctx, a, store.AppealFilter{Status: store.AppealOpen}, store.Page{}); len(open) != 1 {
		t.Fatal("open filter")
	}
	n, err := s.PurgeAppeals(ctx, a, t0.Add(90*time.Second), t0.Add(90*time.Minute))
	check(t, err)
	if n != 2 {
		t.Fatalf("purged %d", n)
	}
	rest, _, _ := s.ListAppeals(ctx, a, store.AppealFilter{}, store.Page{})
	for _, r := range rest {
		if r.Contact != "" && !r.DecidedAt.IsZero() && r.DecidedAt.Before(t0.Add(90*time.Minute)) {
			t.Fatal("contact not cleared")
		}
	}
}

func testLabels(t *testing.T, s store.Store) {
	a := newTenant(t, s, "A")
	player, other := uuid(), uuid()
	v := verdict(a, uuid(), player, pb.Action_ACTION_BAN, t0)
	_, _ = s.UpsertVerdict(ctx, v)
	check(t, s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: player, VerdictID: v.ID, Label: store.LabelCheat, Source: store.SourceConfirmed,
		WindowStart: t0.Add(-time.Hour), WindowEnd: t0, CreatedAt: t0}))
	check(t, s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: other, Label: store.LabelLegit, Source: store.SourceManual,
		WindowStart: t0, WindowEnd: t0.Add(time.Hour), CreatedAt: t0.Add(time.Second)}))
	if c, err := s.HasLegitCover(ctx, a, other, t0.Add(30*time.Minute), t0.Add(2*time.Hour)); err != nil || !c {
		t.Fatalf("overlapping legit label: %v %v", c, err)
	}
	if c, _ := s.HasLegitCover(ctx, a, other, t0.Add(2*time.Hour), t0.Add(3*time.Hour)); c {
		t.Fatal("non-overlapping window covered")
	}
	if c, _ := s.HasLegitCover(ctx, a, player, t0.Add(-time.Hour), t0); c {
		t.Fatal("cheat label counted as legit")
	}
	check(t, s.SetVerdictStatus(ctx, a, v.ID, store.StatusOverturned))
	if c, _ := s.HasLegitCover(ctx, a, player, t0.Add(-time.Minute), t0.Add(time.Minute)); !c {
		t.Fatal("overturned verdict must cover")
	}
	ex, next, err := s.ExportLabels(ctx, a, t0.Add(-time.Hour), store.Page{})
	check(t, err)
	if len(ex) != 2 || next != "" || ex[0].VerdictID != v.ID || ex[0].Action != pb.Action_ACTION_BAN || len(ex[0].Checks) != 2 || len(ex[0].Evidence) == 0 || ex[1].Evidence != nil {
		t.Fatalf("export %+v", ex)
	}
	check(t, s.BeginErasure(ctx, a, other, t0))
	ex, _, _ = s.ExportLabels(ctx, a, time.Time{}, store.Page{})
	if len(ex) != 1 || ex[0].PlayerUUID != player {
		t.Fatal("erased subject exported")
	}
	page1, next, _ := s.ExportLabels(ctx, a, time.Time{}, store.Page{Limit: 1})
	if len(page1) != 1 || next == "" {
		t.Fatal("export paging")
	}
	if n, _ := s.PurgeLabels(ctx, a, t0.Add(time.Millisecond)); n != 1 {
		t.Fatalf("purge labels %d", n)
	}
}

func testClips(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	player, target := uuid(), uuid()
	c := &store.Clip{ID: uuid(), TenantID: a, ServerID: uuid(), PlayerUUID: player, StartUs: 1, EndUs: 25_000_000, Codec: store.ClipCodecZstd,
		Data: []byte("zstd"), Subjects: []string{target}, CreatedAt: t0}
	check(t, s.SaveClip(ctx, c))
	wantErr(t, s.SaveClip(ctx, c), store.ErrConflict)
	got, err := s.GetClip(ctx, a, c.ID)
	check(t, err)
	if string(got.Data) != "zstd" || len(got.Subjects) != 1 || got.Subjects[0] != target || got.EndUs != 25_000_000 || got.Codec != 1 {
		t.Fatalf("clip %+v", got)
	}
	_, err = s.GetClip(ctx, b, c.ID)
	wantErr(t, err, store.ErrNotFound)
	wantErr(t, s.DeleteClip(ctx, b, c.ID), store.ErrNotFound)
	check(t, s.DeleteClip(ctx, a, c.ID))
	wantErr(t, s.DeleteClip(ctx, a, c.ID), store.ErrNotFound)

	// Retention: four old clips; three are protected by an active ban, an open appeal and a label.
	mkClip := func() string {
		id := uuid()
		check(t, s.SaveClip(ctx, &store.Clip{ID: id, TenantID: a, ServerID: "s", PlayerUUID: player, Codec: 1, Data: []byte{1}, CreatedAt: t0}))
		return id
	}
	banned, appealed, labelled, free := mkClip(), mkClip(), mkClip(), mkClip()
	mkVerdict := func(clip string) *store.Verdict {
		v := verdict(a, "s", player, pb.Action_ACTION_BAN, t0)
		v.ClipIDs = []string{clip}
		_, err := s.UpsertVerdict(ctx, v)
		check(t, err)
		return v
	}
	vb := mkVerdict(banned)
	_, err = s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: "s", VerdictID: vb.ID, Executed: true})
	check(t, err)
	va := mkVerdict(appealed)
	check(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: va.ID, PlayerUUID: player, Message: "m"}, store.DefaultAppealLimits))
	vl := mkVerdict(labelled)
	check(t, s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: player, VerdictID: vl.ID, Label: store.LabelCheat, Source: store.SourceConfirmed, WindowStart: t0, WindowEnd: t0}))
	mkVerdict(free)
	n, err := s.PurgeClips(ctx, a, t0.Add(time.Hour))
	check(t, err)
	if n != 1 {
		t.Fatalf("purged %d clips", n)
	}
	for _, id := range []string{banned, appealed, labelled} {
		if _, err := s.GetClip(ctx, a, id); err != nil {
			t.Fatalf("protected clip deleted: %v", err)
		}
	}
	_, err = s.GetClip(ctx, a, free)
	wantErr(t, err, store.ErrNotFound)
}

func testAPIKeys(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	id, secret, _ := identity.NewAPIKey()
	check(t, s.CreateAPIKey(ctx, &store.APIKey{ID: id, TenantID: a, Name: "ci", KeyHash: identity.HashSecret(secret.Reveal()),
		Scopes: []string{store.ScopeRead, store.ScopeVerdictsWrite}, CreatedAt: t0}))
	wantErr(t, s.CreateAPIKey(ctx, &store.APIKey{ID: id, TenantID: a, KeyHash: []byte("x")}), store.ErrConflict)
	k, err := s.VerifyAPIKey(ctx, id, secret, t0.Add(time.Minute))
	check(t, err)
	if k.TenantID != a || !k.HasScope(store.ScopeVerdictsWrite) || k.HasScope(store.ScopeErase) || !k.LastUsedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("key %+v", k)
	}
	if k, _ := s.VerifyAPIKey(ctx, id, secret, t0.Add(time.Minute+time.Second)); !k.LastUsedAt.Equal(t0.Add(time.Minute)) {
		t.Fatal("last_used_at must be throttled to once a minute")
	}
	_, err = s.VerifyAPIKey(ctx, id, identity.Secret("wrong"), t0)
	wantErr(t, err, store.ErrNotFound)
	_, err = s.VerifyAPIKey(ctx, "unknown", secret, t0)
	wantErr(t, err, store.ErrNotFound)
	id2, secret2, _ := identity.NewAPIKey()
	check(t, s.CreateAPIKey(ctx, &store.APIKey{ID: id2, TenantID: a, KeyHash: identity.HashSecret(secret2.Reveal()), CreatedAt: t0.Add(time.Second)}))
	keys, err := s.ListAPIKeys(ctx, a)
	check(t, err)
	if len(keys) != 2 || keys[0].ID != id2 || !keys[0].HasScope(store.ScopeRead) {
		t.Fatalf("keys %+v", keys)
	}
	wantErr(t, s.RevokeAPIKey(ctx, b, id, t0), store.ErrNotFound)
	check(t, s.RevokeAPIKey(ctx, a, id, t0))
	_, err = s.VerifyAPIKey(ctx, id, secret, t0)
	wantErr(t, err, store.ErrNotFound)
}

func testReputation(t *testing.T, s store.Store) {
	a, b, c := newTenant(t, s, "A"), newTenant(t, s, "B"), newTenant(t, s, "C")
	player := uuid()
	for _, x := range []store.ReputationContrib{
		{TenantID: a, PlayerUUID: player, VerdictID: "v1", Weight: 1, CreatedAt: t0},
		{TenantID: b, PlayerUUID: player, VerdictID: "v2", Weight: 1, CreatedAt: t0},
		{TenantID: b, PlayerUUID: player, VerdictID: "v3", Weight: 0.5, CreatedAt: t0.Add(time.Hour)},
		{TenantID: b, PlayerUUID: player, VerdictID: "v3", Weight: 0.5, CreatedAt: t0.Add(time.Hour)},
		{TenantID: c, PlayerUUID: uuid(), VerdictID: "v4", Weight: 1, CreatedAt: t0},
	} {
		check(t, s.AddReputationContrib(ctx, x))
	}
	rep, err := s.PlayerReputation(ctx, c, player)
	check(t, err)
	if len(rep) != 2 {
		t.Fatalf("reputation %+v", rep)
	}
	for _, r := range rep {
		if r.TenantID == b && (r.Weight != 1.5 || r.Contributions != 2 || !r.Latest.Equal(t0.Add(time.Hour))) {
			t.Fatalf("aggregate %+v", r)
		}
	}
	if rep, _ := s.PlayerReputation(ctx, a, player); len(rep) != 1 || rep[0].TenantID != b {
		t.Fatal("requesting tenant must be excluded")
	}
	if n, _ := s.RemoveReputationContrib(ctx, b, "v3"); n != 1 {
		t.Fatal("remove")
	}
	if n, _ := s.RemoveReputationContrib(ctx, a, "v2"); n != 0 {
		t.Fatal("removed another tenant's contribution")
	}
	check(t, s.UpsertPlayer(ctx, a, uuid(), "p1", "s", t0))
	check(t, s.UpsertPlayer(ctx, a, uuid(), "p2", "s", t0))
	ti, err := s.TrustInputs(ctx, a)
	check(t, err)
	if ti.Plan != store.PlanFree || ti.DistinctPlayers != 2 || ti.OptIn || !ti.CreatedAt.Equal(t0) {
		t.Fatalf("trust %+v", ti)
	}
}

func testUsage(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	check(t, s.RecordPeakPlayers(ctx, a, t0, 10))
	check(t, s.RecordPeakPlayers(ctx, a, t0.Add(3*time.Hour), 7))
	check(t, s.RecordPeakPlayers(ctx, a, t0.Add(6*time.Hour), 12))
	check(t, s.RecordPeakPlayers(ctx, a, t0.Add(24*time.Hour), 3))
	check(t, s.RecordPeakPlayers(ctx, b, t0, 99))
	days, err := s.ListUsage(ctx, a, t0.Add(-48*time.Hour))
	check(t, err)
	if len(days) != 2 || days[0].PeakPlayers != 12 || days[1].PeakPlayers != 3 || days[0].Day.Hour() != 0 {
		t.Fatalf("usage %+v", days)
	}
	if n, _ := s.PurgeUsage(ctx, a, t0.Add(24*time.Hour)); n != 1 {
		t.Fatal("purge usage")
	}
}

func testProbeStats(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	srv := uuid()
	check(t, s.RecordProbeStats(ctx, a, srv, t0.Add(10*time.Second), store.ProbeBucket{Count: 3, P50: 30_000, P95: 60_000, P99: 90_000, Max: 95_000}))
	check(t, s.RecordProbeStats(ctx, a, srv, t0.Add(50*time.Second), store.ProbeBucket{Count: 1, P50: 70_000, P95: 100_000, P99: 130_000, Max: 140_000}))
	check(t, s.RecordProbeStats(ctx, a, uuid(), t0.Add(time.Minute), store.ProbeBucket{Count: 4, P50: 10_000, P95: 20_000, P99: 30_000, Max: 40_000}))
	check(t, s.RecordProbeStats(ctx, b, srv, t0, store.ProbeBucket{Count: 8, P50: 1_000, P95: 2_000, P99: 3_000, Max: 4_000}))
	check(t, s.RecordProbeStats(ctx, a, srv, t0, store.ProbeBucket{}))
	rows, err := s.ListProbeStats(ctx, a, srv, t0)
	check(t, err)
	if len(rows) != 1 || rows[0].Count != 4 || rows[0].P50 != 40_000 || rows[0].P95 != 70_000 || rows[0].Max != 140_000 || !rows[0].Minute.Equal(t0) {
		t.Fatalf("merged bucket %+v", rows)
	}
	if all, _ := s.ListProbeStats(ctx, a, "", t0); len(all) != 2 {
		t.Fatal("all servers")
	}
	sum, err := s.ProbeSummary(ctx, a, t0)
	check(t, err)
	if sum.Count != 8 || sum.P50 != 25_000 || sum.Max != 140_000 {
		t.Fatalf("summary %+v", sum)
	}
	g, err := s.GlobalProbeSummary(ctx, t0)
	check(t, err)
	if g.Count != 16 || g.Max != 140_000 {
		t.Fatalf("global %+v", g)
	}
	if empty, _ := s.ProbeSummary(ctx, a, t0.Add(time.Hour)); empty.Count != 0 {
		t.Fatal("empty summary")
	}
	if n, _ := s.PurgeProbeStats(ctx, a, t0.Add(time.Minute)); n != 1 {
		t.Fatal("purge probe stats")
	}
}

func testAudit(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	for i := 0; i < 5; i++ {
		check(t, s.InsertAudit(ctx, &store.AuditEntry{TenantID: a, Principal: "user:alice", Action: "tenant.update",
			Target: a, Detail: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)), CreatedAt: t0.Add(time.Duration(i) * time.Second)}))
	}
	check(t, s.InsertAudit(ctx, &store.AuditEntry{TenantID: b, Principal: "system", Action: "x", CreatedAt: t0}))
	p1, next, err := s.ListAudit(ctx, a, store.Page{Limit: 3})
	check(t, err)
	p2, next2, err := s.ListAudit(ctx, a, store.Page{Limit: 3, Cursor: next})
	check(t, err)
	if len(p1) != 3 || len(p2) != 2 || next2 != "" || string(p1[0].Detail) == "" {
		t.Fatalf("audit pages %d %d", len(p1), len(p2))
	}
	var d map[string]int
	if err := json.Unmarshal(p1[0].Detail, &d); err != nil || d["i"] != 4 {
		t.Fatalf("newest first: %s", p1[0].Detail)
	}
	if n, _ := s.PurgeAudit(ctx, a, t0.Add(2*time.Second)); n != 2 {
		t.Fatal("purge audit")
	}
}

func testErasure(t *testing.T, s store.Store) {
	a, b := newTenant(t, s, "A"), newTenant(t, s, "B")
	player, bystander := uuid(), uuid()
	srv := uuid()
	check(t, s.UpsertPlayer(ctx, a, player, "Cheater", srv, t0))
	check(t, s.UpsertPlayer(ctx, b, player, "Cheater", srv, t0))
	own := &store.Clip{ID: uuid(), TenantID: a, ServerID: srv, PlayerUUID: player, Codec: 1, Data: []byte{1}, CreatedAt: t0}
	asTarget := &store.Clip{ID: uuid(), TenantID: a, ServerID: srv, PlayerUUID: bystander, Codec: 1, Data: []byte{1}, Subjects: []string{player}, CreatedAt: t0}
	unrelated := &store.Clip{ID: uuid(), TenantID: a, ServerID: srv, PlayerUUID: bystander, Codec: 1, Data: []byte{1}, CreatedAt: t0}
	for _, c := range []*store.Clip{own, asTarget, unrelated} {
		check(t, s.SaveClip(ctx, c))
	}
	active := verdict(a, srv, player, pb.Action_ACTION_BAN, t0)
	active.ClipIDs = []string{own.ID}
	old := verdict(a, srv, player, pb.Action_ACTION_BAN, t0)
	alert := verdict(a, srv, player, pb.Action_ACTION_ALERT, t0)
	for _, v := range []*store.Verdict{active, old, alert} {
		_, err := s.UpsertVerdict(ctx, v)
		check(t, err)
	}
	for _, v := range []*store.Verdict{active, old} {
		_, err := s.ApplyAck(ctx, store.Ack{TenantID: a, ServerID: srv, VerdictID: v.ID, Executed: true})
		check(t, err)
	}
	check(t, s.OverturnBan(ctx, a, old.ID, store.BanOverturn{By: "user:x"}))
	check(t, s.CreateAppeal(ctx, &store.Appeal{TenantID: a, VerdictID: active.ID, PlayerUUID: player, Message: "m", Contact: "c"}, store.DefaultAppealLimits))
	check(t, s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: player, VerdictID: active.ID, Label: store.LabelCheat, Source: store.SourceConfirmed, WindowStart: t0, WindowEnd: t0}))
	check(t, s.AddReputationContrib(ctx, store.ReputationContrib{TenantID: a, PlayerUUID: player, VerdictID: active.ID, Weight: 1}))

	check(t, s.BeginErasure(ctx, a, player, t0))
	counts, err := s.EraseSubject(ctx, a, player)
	check(t, err)
	want := map[string]int64{"players": 1, "player_names": 1, "clips": 2, "verdicts": 2, "verdicts_minimised": 1, "bans": 1, "appeals": 1, "labels": 1, "reputation_contrib": 1}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("counts[%s] = %d, want %d (all: %v)", k, counts[k], n, counts)
		}
	}
	_, err = s.GetPlayer(ctx, a, player)
	wantErr(t, err, store.ErrNotFound)
	if _, err := s.GetPlayer(ctx, b, player); err != nil {
		t.Fatal("other tenant's player erased")
	}
	if _, err := s.GetClip(ctx, a, unrelated.ID); err != nil {
		t.Fatal("unrelated clip erased")
	}
	_, err = s.GetClip(ctx, a, asTarget.ID)
	wantErr(t, err, store.ErrNotFound)
	kept, err := s.GetVerdict(ctx, a, active.ID)
	check(t, err)
	if kept.PlayerName != "" || kept.Reason != "" || string(kept.Evidence) != "{}" || len(kept.ClipIDs) != 0 || kept.Payload != nil ||
		kept.Score != 130 || len(kept.Checks) != 2 || kept.PlayerUUID != player {
		t.Fatalf("minimal record %+v", kept)
	}
	if bn, err := s.GetBan(ctx, a, active.ID); err != nil || !bn.Active || bn.Reason != "" {
		t.Fatalf("active ban must stay (minimal): %+v %v", bn, err)
	}
	_, err = s.GetVerdict(ctx, a, old.ID)
	wantErr(t, err, store.ErrNotFound)

	check(t, s.CompleteErasure(ctx, a, player, counts, t0.Add(time.Minute)))
	e, err := s.GetErasure(ctx, a, player)
	check(t, err)
	if !e.CompletedAt.Equal(t0.Add(time.Minute)) || e.Counts["clips"] != 2 {
		t.Fatalf("erasure %+v", e)
	}
	check(t, s.BeginErasure(ctx, a, player, t0.Add(time.Hour)))
	e, _ = s.GetErasure(ctx, a, player)
	if !e.CompletedAt.IsZero() || !e.RequestedAt.Equal(t0.Add(time.Hour)) {
		t.Fatal("restart must reset completion")
	}
	list, err := s.ListErasures(ctx, a, t0)
	check(t, err)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	_, err = s.GetErasure(ctx, b, player)
	wantErr(t, err, store.ErrNotFound)
}

func testMeta(t *testing.T, s store.Store) {
	_, err := s.GetMeta(ctx, store.MetaLicensePepper)
	wantErr(t, err, store.ErrNotFound)
	v, err := s.PutMetaIfAbsent(ctx, store.MetaLicensePepper, []byte("first"))
	check(t, err)
	v2, err := s.PutMetaIfAbsent(ctx, store.MetaLicensePepper, []byte("second"))
	check(t, err)
	if string(v) != "first" || string(v2) != "first" {
		t.Fatalf("meta %q %q", v, v2)
	}
	got, err := s.GetMeta(ctx, store.MetaLicensePepper)
	if err != nil || string(got) != "first" {
		t.Fatal("get meta")
	}
}

func testRetention(t *testing.T, s store.Store) {
	a := newTenant(t, s, "A")
	player := uuid()
	old := t0.Add(-800 * 24 * time.Hour)
	v := verdict(a, "s", player, pb.Action_ACTION_ALERT, old)
	_, _ = s.UpsertVerdict(ctx, v)
	check(t, s.InsertAudit(ctx, &store.AuditEntry{TenantID: a, Principal: "system", Action: "x", CreatedAt: old}))
	check(t, s.RecordPeakPlayers(ctx, a, old, 5))
	check(t, s.RecordProbeStats(ctx, a, "s", old, store.ProbeBucket{Count: 1}))
	check(t, s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: player, Label: store.LabelLegit, Source: store.SourceManual, WindowStart: old, WindowEnd: old, CreatedAt: old}))
	check(t, s.SaveClip(ctx, &store.Clip{ID: uuid(), TenantID: a, ServerID: "s", PlayerUUID: player, Codec: 1, Data: []byte{1}, CreatedAt: old}))
	counts, err := store.Purge(ctx, s, a, t0, store.DefaultRetention)
	check(t, err)
	for _, table := range []string{"clips", "verdicts", "labels", "usage_daily", "probe_stats", "audit_log"} {
		if counts[table] != 1 {
			t.Errorf("%s purged %d", table, counts[table])
		}
	}
}

func testConcurrentWrites(t *testing.T, s store.Store) {
	a := newTenant(t, s, "A")
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := uuid()
			if err := s.UpsertPlayer(ctx, a, p, fmt.Sprintf("p%d", i), "s", t0); err != nil {
				errs <- err
				return
			}
			if _, err := s.GetPlayer(ctx, a, p); err != nil {
				errs <- err
			}
			if err := s.RecordPeakPlayers(ctx, a, t0, i); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if days, _ := s.ListUsage(ctx, a, t0); len(days) != 1 || days[0].PeakPlayers != 31 {
		t.Fatalf("max-upsert under concurrency: %+v", days)
	}
}
