package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

const (
	playerMissed = "aaaaaaaa-0000-4000-8000-000000000009" // no verdicts
	playerOther  = "aaaaaaaa-0000-4000-8000-000000000010" // no verdicts
)

func cheatBan(player string, entry int, start time.Time) *store.ModerationLabel {
	return &store.ModerationLabel{TenantID: tenantA, Source: sourceLiteBans, EntryID: strconv.Itoa(entry), Kind: store.ModerationBanAdded,
		Label: store.LabelCheat, PlayerUUID: player, BanStart: start, Permanent: true, Executor: store.ExecutorStaff}
}

func wrongfulUnban(player string, entry int, start time.Time) *store.ModerationLabel {
	m := cheatBan(player, entry, start)
	m.Kind, m.Label, m.RemovedAt = store.ModerationBanRemoved, store.LabelLegit, fixedNow.Add(-time.Minute)
	return m
}

// inNetwork sets the network id of m's sending server.
func inNetwork(m *store.ModerationLabel, networkID string) *store.ModerationLabel {
	m.NetworkID = networkID
	return m
}

func (e *env) applyLabel(m *store.ModerationLabel) {
	e.t.Helper()
	check(e.t, e.s.applyModerationLabel(context.Background(), m, fixedNow))
}

// playerLabels returns the tenant's labels of player keyed "<verdict id or source>/<label>".
func playerLabels(t *testing.T, e *env, tenant, player string) map[string]store.LabelExport {
	t.Helper()
	ls, _, err := e.d.Store.ExportLabels(context.Background(), tenant, time.Time{}, store.Page{Limit: 200})
	check(t, err)
	out := map[string]store.LabelExport{}
	for _, l := range ls {
		if l.PlayerUUID != player {
			continue
		}
		k := l.VerdictID
		if k == "" {
			k = l.Source
		}
		k += "/" + l.Label.Label
		if _, dup := out[k]; dup {
			t.Fatalf("two %s labels", k)
		}
		out[k] = l
	}
	return out
}

func wantDecision(t *testing.T, e *env, id, status, by string) {
	t.Helper()
	v, err := e.d.Store.GetVerdict(context.Background(), tenantA, id)
	check(t, err)
	if v.Status != status || v.DecidedBy != by {
		t.Fatalf("verdict %s: %s by %q, want %s by %q", id, v.Status, v.DecidedBy, status, by)
	}
}

func auditActions(t *testing.T, e *env, action string) []store.AuditEntry {
	t.Helper()
	es, _, err := e.d.Store.ListAudit(context.Background(), tenantA, store.Page{Limit: 200})
	check(t, err)
	var out []store.AuditEntry
	for _, a := range es {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

// A LiteBans cheat ban confirms every verdict of the player created in [ban - lookback, ban] (bounds
// included) through the confirm path, decided by LiteBans, and nothing outside it; duplicates (another
// server, a backfill) change nothing.
func TestLiteBansCheatBanConfirms(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	week := 7 * 24 * time.Hour
	edge := verdictFixture(tenantA, "0b000000-0000-5000-8000-0000000000b1", srv1, player1, "Steve", pb.Action_ACTION_ALERT, ban.Add(-week))
	early := verdictFixture(tenantA, "0b000000-0000-5000-8000-0000000000b2", srv1, player1, "Steve", pb.Action_ACTION_ALERT, ban.Add(-week-time.Microsecond))
	after := verdictFixture(tenantA, "0b000000-0000-5000-8000-0000000000b3", srv1, player1, "Steve", pb.Action_ACTION_ALERT, ban.Add(time.Microsecond))
	unmit := verdictFixture(tenantA, "0b000000-0000-5000-8000-0000000000b4", srv1, player1, "Steve", pb.Action_ACTION_UNMITIGATE, ban.Add(-time.Minute))
	for _, v := range []*store.Verdict{edge, early, after, unmit} {
		_, err := e.d.Store.UpsertVerdict(ctx, v)
		check(t, err)
	}
	e.applyLabel(cheatBan(player1, 4521, ban))

	for _, id := range []string{vBan, vAlert, edge.ID} {
		wantDecision(t, e, id, store.StatusConfirmed, store.DecidedByLiteBans)
	}
	for _, id := range []string{early.ID, after.ID, unmit.ID} {
		wantDecision(t, e, id, store.StatusOpen, "")
	}
	ls := playerLabels(t, e, tenantA, player1)
	if len(ls) != 3 {
		t.Fatalf("labels %v", ls)
	}
	for _, id := range []string{vBan, vAlert, edge.ID} {
		l, ok := ls[id+"/cheat"]
		if !ok || l.Source != store.SourceLiteBans {
			t.Fatalf("label of %s: %+v", id, l)
		}
	}
	// The label window is the verdict's signal window (the confirm path's).
	v, _ := e.d.Store.GetVerdict(ctx, tenantA, vBan)
	from, to := signalWindow(v)
	if l := ls[vBan+"/cheat"]; !l.WindowStart.Equal(from) || !l.WindowEnd.Equal(to) {
		t.Fatalf("window %v..%v, want %v..%v", l.WindowStart, l.WindowEnd, from, to)
	}
	row, err := e.d.Store.GetModerationLabel(ctx, tenantA, "", sourceLiteBans, "4521", store.ModerationBanAdded)
	check(t, err)
	if row.AppliedAt.IsZero() || row.Verdicts != 3 {
		t.Fatalf("row %+v", row)
	}
	audits := auditActions(t, e, "litebans.cheat")
	if len(audits) != 1 || audits[0].Principal != "litebans" || audits[0].Target != "litebans:4521" {
		t.Fatalf("audit %+v", audits)
	}
	// A confirm changes nothing on the servers.
	if ban, _ := e.d.Store.GetBan(ctx, tenantA, vBan); !ban.Active {
		t.Fatal("ban lifted by a confirm")
	}
	if recs := e.verdictRecords(tenantA); len(recs) != 0 {
		t.Fatalf("records %v", recs)
	}
	// The dashboard sees who decided.
	got := e.must(200, "GET", "/api/v1/verdicts/"+vBan, nil, as(tenantA, "viewer")).json(t)
	if got["status"] != store.StatusConfirmed || got["decided_by"] != store.DecidedByLiteBans {
		t.Fatalf("verdict json: %v", got)
	}

	// The same entry from the proxy, and again after a plugin restart: nothing changes.
	for i := 0; i < 2; i++ {
		e.applyLabel(cheatBan(player1, 4521, ban))
	}
	if n := len(auditActions(t, e, "litebans.cheat")); n != 1 {
		t.Fatalf("a duplicate was applied again: %d audit entries", n)
	}
	if ls := playerLabels(t, e, tenantA, player1); len(ls) != 3 {
		t.Fatalf("labels after duplicates %v", ls)
	}
}

// A wrongful unban of the entry supersedes its cheat labels and overturns the verdicts through the
// overturn path: legit labels, the executed BAN lifted with an UNBAN, UNMITIGATE, no contradiction.
func TestLiteBansWrongfulUnbanOverturns(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	e.connect(tenantA, srv1, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	ob := e.outbox(tenantA)
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(player1, 4521, ban))
	e.applyLabel(wrongfulUnban(player1, 4521, ban))

	for _, id := range []string{vBan, vAlert} {
		wantDecision(t, e, id, store.StatusOverturned, store.DecidedByLiteBans)
	}
	ls := playerLabels(t, e, tenantA, player1)
	if len(ls) != 2 || ls[vBan+"/legit"].Source != store.SourceLiteBans || ls[vAlert+"/legit"].Source != store.SourceLiteBans {
		t.Fatalf("labels after the unban: %v", ls)
	}
	b, err := e.d.Store.GetBan(ctx, tenantA, vBan)
	check(t, err)
	unbanID := identity.DerivedVerdictID(vBan, int32(pb.Action_ACTION_UNBAN))
	if b.Active || b.OverturnedBy != "litebans" || b.OverturnNote != "LiteBans ban #4521" || b.UnbanVerdictID != unbanID || b.UnbanState != store.UnbanPending {
		t.Fatalf("ban %+v", b)
	}
	unban, err := e.d.Store.GetVerdict(ctx, tenantA, unbanID)
	check(t, err)
	if unban.ServerID != srv1 || unban.Reason != "Ban lifted as wrong in LiteBans" || unban.Supersedes != vBan {
		t.Fatalf("unban %+v", unban)
	}
	for _, id := range []string{vBan, vAlert} {
		um, err := e.d.Store.GetVerdict(ctx, tenantA, identity.DerivedVerdictID(id, int32(pb.Action_ACTION_UNMITIGATE)))
		if err != nil || um.Reason != "Verdict overturned by a LiteBans unban" {
			t.Fatalf("unmitigate of %s: %+v %v", id, um, err)
		}
	}
	sent := false
	for _, o := range ob.all(500 * time.Millisecond) {
		sent = sent || o.GetVerdict().GetVerdictId() == unbanID
	}
	if !sent {
		t.Fatal("UNBAN not sent to the connected executing server")
	}
	if cover, _ := e.d.Store.HasLegitCover(ctx, tenantA, player1, fixedNow.Add(-2*time.Hour-time.Minute), fixedNow.Add(-2*time.Hour)); !cover {
		t.Fatal("no legit cover after the wrongful unban")
	}
	row, _ := e.d.Store.GetModerationLabel(ctx, tenantA, "", sourceLiteBans, "4521", store.ModerationBanRemoved)
	if row.Verdicts != 2 || !row.RemovedAt.Equal(fixedNow.Add(-time.Minute)) {
		t.Fatalf("row %+v", row)
	}
	e.applyLabel(wrongfulUnban(player1, 4521, ban))
	if n := len(auditActions(t, e, "litebans.legit")); n != 1 {
		t.Fatalf("legit audit entries %d", n)
	}
}

// Without a verdict in the lookback the ban labels the window before it (unflagged); its wrongful
// unban replaces that cheat label by a legit one over the same window.
func TestLiteBansUnflaggedWindow(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(playerMissed, 7, ban))
	ls := playerLabels(t, e, tenantA, playerMissed)
	l, ok := ls[store.SourceLiteBansUnflagged+"/cheat"]
	if len(ls) != 1 || !ok || l.VerdictID != "" || !l.WindowStart.Equal(ban.Add(-2*time.Hour)) || !l.WindowEnd.Equal(ban) {
		t.Fatalf("unflagged cheat label %v", ls)
	}
	if row, _ := e.d.Store.GetModerationLabel(context.Background(), tenantA, "", sourceLiteBans, "7", store.ModerationBanAdded); row.Verdicts != 0 || row.AppliedAt.IsZero() {
		t.Fatalf("row %+v", row)
	}
	e.applyLabel(wrongfulUnban(playerMissed, 7, ban))
	ls = playerLabels(t, e, tenantA, playerMissed)
	l, ok = ls[store.SourceLiteBansUnflagged+"/legit"]
	if len(ls) != 1 || !ok || !l.WindowStart.Equal(ban.Add(-2*time.Hour)) || !l.WindowEnd.Equal(ban) {
		t.Fatalf("after the unban %v", ls)
	}
	// The export carries the sources (ml/FEATURES.md 7).
	r := e.must(200, "GET", "/api/v1/export/labels", nil, as(tenantA, "viewer"))
	if !strings.Contains(string(r.body), `"source":"litebans_unflagged"`) {
		t.Fatalf("export: %s", r.body)
	}
}

// Staff decisions win: a LiteBans label never changes a verdict staff confirmed or overturned, while
// staff take over a verdict (and its label) LiteBans decided.
func TestLiteBansNeverReplaceStaffDecisions(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ban := fixedNow.Add(-time.Hour)
	e.must(200, "POST", "/api/v1/verdicts/"+vAlert+"/confirm", nil, as(tenantA, "staff"))
	e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/overturn", nil, as(tenantA, "staff"))
	before := playerLabels(t, e, tenantA, player1)
	e.applyLabel(cheatBan(player1, 1, ban))
	wantDecision(t, e, vAlert, store.StatusConfirmed, store.DecidedByStaff)
	wantDecision(t, e, vBan, store.StatusOverturned, store.DecidedByStaff)
	e.applyLabel(wrongfulUnban(player1, 1, ban))
	wantDecision(t, e, vAlert, store.StatusConfirmed, store.DecidedByStaff)
	after := playerLabels(t, e, tenantA, player1)
	if len(after) != 2 || after[vAlert+"/cheat"].Source != store.SourceConfirmed || after[vBan+"/legit"].Source != store.SourceOverturned ||
		len(before) != len(after) {
		t.Fatalf("staff labels changed: %v -> %v", before, after)
	}
	if row, _ := e.d.Store.GetModerationLabel(context.Background(), tenantA, "", sourceLiteBans, "1", store.ModerationBanRemoved); row.Verdicts != 0 {
		t.Fatalf("decided %d staff verdicts", row.Verdicts)
	}

	// LiteBans decides player2's verdicts first; staff then overturn one and confirm the other.
	e.applyLabel(cheatBan(player2, 2, ban))
	wantDecision(t, e, vShadow, store.StatusConfirmed, store.DecidedByLiteBans)
	wantDecision(t, e, vMit, store.StatusConfirmed, store.DecidedByLiteBans)
	e.must(200, "POST", "/api/v1/verdicts/"+vShadow+"/overturn", nil, as(tenantA, "staff"))
	r := e.must(200, "POST", "/api/v1/verdicts/"+vMit+"/confirm", nil, as(tenantA, "staff")).json(t)
	if r["decided_by"] != store.DecidedByStaff {
		t.Fatalf("confirm response %v", r)
	}
	ls := playerLabels(t, e, tenantA, player2)
	if len(ls) != 2 || ls[vShadow+"/legit"].Source != store.SourceOverturned || ls[vMit+"/cheat"].Source != store.SourceConfirmed {
		t.Fatalf("staff did not take over: %v", ls)
	}
	e.applyLabel(wrongfulUnban(player2, 2, ban))
	wantDecision(t, e, vShadow, store.StatusOverturned, store.DecidedByStaff)
	wantDecision(t, e, vMit, store.StatusConfirmed, store.DecidedByStaff)
	// A LiteBans overturn is final like a staff one: the dashboard cannot confirm it afterwards, but
	// staff can overturn it again, which takes it over.
	late := verdictFixture(tenantA, "0b000000-0000-5000-8000-0000000000c1", srv1, player1, "Steve", pb.Action_ACTION_KICK, fixedNow.Add(-30*time.Minute))
	_, err := e.d.Store.UpsertVerdict(context.Background(), late)
	check(t, err)
	e.applyLabel(wrongfulUnban(player1, 3, fixedNow))
	wantDecision(t, e, late.ID, store.StatusOverturned, store.DecidedByLiteBans)
	wantDecision(t, e, vAlert, store.StatusConfirmed, store.DecidedByStaff)
	if r := e.do("POST", "/api/v1/verdicts/"+late.ID+"/confirm", nil, as(tenantA, "staff")); r.status != 409 {
		t.Fatalf("confirming a verdict LiteBans overturned: %d", r.status)
	}
	e.must(200, "POST", "/api/v1/verdicts/"+late.ID+"/overturn", nil, as(tenantA, "staff"))
	wantDecision(t, e, late.ID, store.StatusOverturned, store.DecidedByStaff)
	if l := playerLabels(t, e, tenantA, player1)[late.ID+"/legit"]; l.Source != store.SourceOverturned {
		t.Fatalf("legit label after the staff overturn: %+v", l)
	}
}

// A wrongful unban seen before its ban supersedes it; PE_LABELS_LOOKBACK and PE_LABELS_WINDOW set the
// intervals.
func TestLiteBansOrderAndIntervals(t *testing.T) {
	e := newEnv(t, "PE_LABELS_LOOKBACK=1d", "PE_LABELS_WINDOW=30m")
	e.seed()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(wrongfulUnban(player2, 3, ban))
	wantDecision(t, e, vShadow, store.StatusOverturned, store.DecidedByLiteBans) // 3 h before the ban
	wantDecision(t, e, vMit, store.StatusOpen, "")                               // 29 h before: outside 1 d
	e.applyLabel(cheatBan(player2, 3, ban))
	wantDecision(t, e, vMit, store.StatusOpen, "")
	ls := playerLabels(t, e, tenantA, player2)
	if len(ls) != 1 || ls[vShadow+"/legit"].Source != store.SourceLiteBans {
		t.Fatalf("labels %v", ls)
	}
	row, _ := e.d.Store.GetModerationLabel(context.Background(), tenantA, "", sourceLiteBans, "3", store.ModerationBanAdded)
	if row.AppliedAt.IsZero() || row.Verdicts != 0 {
		t.Fatalf("superseded ban row %+v", row)
	}
	e.applyLabel(cheatBan(playerMissed, 4, ban))
	if l := playerLabels(t, e, tenantA, playerMissed)[store.SourceLiteBansUnflagged+"/cheat"]; !l.WindowStart.Equal(ban.Add(-30 * time.Minute)) {
		t.Fatalf("unflagged window %+v", l)
	}
}

// A wrongful unban that reaches the cloud late (a backfill after an outage or on first install) still
// overturns, but runs no unban command: the player may have a newer ban by then, which it would lift.
func TestLiteBansLateUnbanLiftsNoBan(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	e.connect(tenantA, srv1, stream.ConnectionInfo{Platform: pb.Platform_PLATFORM_PAPER})
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(player1, 4521, ban))
	late := wrongfulUnban(player1, 4521, ban)
	late.RemovedAt = fixedNow.Add(-liteBansUnbanMaxAge - time.Minute)
	e.applyLabel(late)

	for _, id := range []string{vBan, vAlert} {
		wantDecision(t, e, id, store.StatusOverturned, store.DecidedByLiteBans)
		if _, err := e.d.Store.GetVerdict(ctx, tenantA, identity.DerivedVerdictID(id, int32(pb.Action_ACTION_UNMITIGATE))); err != nil {
			t.Fatalf("no UNMITIGATE for %s: %v", id, err)
		}
	}
	if ls := playerLabels(t, e, tenantA, player1); len(ls) != 2 || ls[vBan+"/legit"].Source != store.SourceLiteBans {
		t.Fatalf("labels %v", ls)
	}
	if _, err := e.d.Store.GetVerdict(ctx, tenantA, identity.DerivedVerdictID(vBan, int32(pb.Action_ACTION_UNBAN))); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late unban issued an UNBAN: %v", err)
	}
	if b, err := e.d.Store.GetBan(ctx, tenantA, vBan); err != nil || !b.Active || b.UnbanVerdictID != "" {
		t.Fatalf("ban %+v %v", b, err)
	}
	// A staff overturn still lifts it.
	e.must(200, "POST", "/api/v1/verdicts/"+vBan+"/overturn", nil, as(tenantA, "staff"))
	if b, _ := e.d.Store.GetBan(ctx, tenantA, vBan); b.Active || b.UnbanVerdictID == "" {
		t.Fatalf("ban after the staff overturn %+v", b)
	}

	for _, c := range []struct {
		removed time.Duration // before receipt; -1 = unknown
		unban   bool
	}{{0, true}, {liteBansUnbanMaxAge, true}, {liteBansUnbanMaxAge + time.Second, false}, {-1, false}, {-time.Minute, true}} {
		m := wrongfulUnban(player1, 1, ban)
		m.ReceivedAt, m.RemovedAt = fixedNow, fixedNow.Add(-c.removed)
		if c.removed == -1 {
			m.RemovedAt = time.Time{}
		}
		if got := liteBansDecision(m).unban; got != c.unban {
			t.Errorf("removed %v before receipt: unban %v, want %v", c.removed, got, c.unban)
		}
	}
}

// The wrongful unban also removes the cheat labels the ban wrote for verdicts purged since.
func TestLiteBansUnbanSupersedesLabelsOfPurgedVerdicts(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(player1, 4521, ban))
	if ls := playerLabels(t, e, tenantA, player1); ls[vAlert+"/cheat"].Source != store.SourceLiteBans {
		t.Fatalf("labels %v", ls)
	}
	_, err := e.d.Store.PurgeVerdicts(ctx, tenantA, fixedNow.Add(-150*time.Minute)) // the ALERT (3 h old), not the active BAN
	check(t, err)
	if _, err := e.d.Store.GetVerdict(ctx, tenantA, vAlert); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ALERT not purged: %v", err)
	}
	e.applyLabel(wrongfulUnban(player1, 4521, ban))
	ls := playerLabels(t, e, tenantA, player1)
	if len(ls) != 1 || ls[vBan+"/legit"].Source != store.SourceLiteBans {
		t.Fatalf("labels after the unban: %v", ls)
	}
}

// Every plugin enable resends the bans of the last backfill-days, also after an erasure: the erased
// player's bans from before it are not recorded, decided or labelled again; a later ban is new data.
func TestLiteBansLabelsOfErasedPlayers(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(player1, 4521, ban))
	e.applyLabel(cheatBan(playerMissed, 7, ban))
	for _, p := range []string{player1, playerMissed} {
		e.must(200, "DELETE", "/api/v1/players/"+p, nil, as(tenantA, "owner"))
	}
	audits := len(auditActions(t, e, "litebans.cheat")) + len(auditActions(t, e, "litebans.legit"))

	// The next backfill: both bans again, a wrongful unban, and a ban issued at the erasure.
	for _, m := range []*store.ModerationLabel{cheatBan(player1, 4521, ban), cheatBan(playerMissed, 7, ban),
		wrongfulUnban(playerMissed, 7, ban), cheatBan(player1, 4522, fixedNow)} {
		e.applyLabel(m)
		if _, err := e.d.Store.GetModerationLabel(ctx, tenantA, "", sourceLiteBans, m.EntryID, m.Kind); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ban #%s %s of an erased player recorded again: %v", m.EntryID, m.Kind, err)
		}
	}
	for _, p := range []string{player1, playerMissed} {
		counts, err := e.d.Store.EraseSubject(ctx, tenantA, p)
		check(t, err)
		if counts["labels"] != 0 || counts["moderation_labels"] != 0 {
			t.Fatalf("%s: labels written again after the erasure: %v", p, counts)
		}
	}
	if n := len(auditActions(t, e, "litebans.cheat")) + len(auditActions(t, e, "litebans.legit")); n != audits {
		t.Fatalf("audit entries %d, want %d", n, audits)
	}

	// A ban after the erasure is applied (the label export still leaves the erased player out).
	e.applyLabel(cheatBan(playerMissed, 8, fixedNow.Add(time.Minute)))
	if row, err := e.d.Store.GetModerationLabel(ctx, tenantA, "", sourceLiteBans, "8", store.ModerationBanAdded); err != nil || row.AppliedAt.IsZero() {
		t.Fatalf("ban after the erasure: %+v %v", row, err)
	}
}

// Two networks of a tenant with their own LiteBans database have the same entry ids. The sending
// server's network id keeps them apart: ban #57 of each network applies to its own player, and a
// wrongful unban in one network neither supersedes the other's labels nor stops the other's ban.
func TestLiteBansNetworksKeepEntriesApart(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(inNetwork(cheatBan(player1, 57, ban), "net-a"))
	e.applyLabel(inNetwork(cheatBan(player2, 57, ban), "net-b"))
	for _, id := range []string{vBan, vAlert, vShadow, vMit} {
		wantDecision(t, e, id, store.StatusConfirmed, store.DecidedByLiteBans)
	}
	for network, player := range map[string]string{"net-a": player1, "net-b": player2} {
		row, err := e.d.Store.GetModerationLabel(ctx, tenantA, network, sourceLiteBans, "57", store.ModerationBanAdded)
		if err != nil || row.PlayerUUID != player || row.AppliedAt.IsZero() || row.Verdicts != 2 {
			t.Fatalf("%s: %+v %v", network, row, err)
		}
	}
	if n := len(auditActions(t, e, "litebans.cheat")); n != 2 {
		t.Fatalf("cheat audit entries %d", n)
	}

	// Network B lifts its #57 as wrong: player2's verdicts are overturned, player1's stay confirmed with
	// their cheat labels, and network A's ban still counts as a cheat ban.
	e.applyLabel(inNetwork(wrongfulUnban(player2, 57, ban), "net-b"))
	for _, id := range []string{vShadow, vMit} {
		wantDecision(t, e, id, store.StatusOverturned, store.DecidedByLiteBans)
	}
	for _, id := range []string{vBan, vAlert} {
		wantDecision(t, e, id, store.StatusConfirmed, store.DecidedByLiteBans)
	}
	if ls := playerLabels(t, e, tenantA, player1); len(ls) != 2 || ls[vBan+"/cheat"].Source != store.SourceLiteBans ||
		ls[vAlert+"/cheat"].Source != store.SourceLiteBans {
		t.Fatalf("network A's labels after network B's unban: %v", ls)
	}
	if b, _ := e.d.Store.GetBan(ctx, tenantA, vBan); !b.Active {
		t.Fatal("network B's unban lifted network A's ban")
	}
	ag, err := e.d.Store.ModerationAgreement(ctx, tenantA, fixedNow.Add(-24*time.Hour), fixedNow, 7*24*time.Hour)
	check(t, err)
	if len(ag.Bans) != 1 || ag.Bans[0].PlayerUUID != player1 || ag.Overturns != 1 || ag.VerdictsOverturned != 2 {
		t.Fatalf("agreement %+v", ag)
	}

	// Network B's unban of #58 arrives before network A's ban #58: A's ban is not lifted by it.
	e.applyLabel(inNetwork(wrongfulUnban(playerOther, 58, ban), "net-b"))
	e.applyLabel(inNetwork(cheatBan(playerMissed, 58, ban), "net-a"))
	e.applyLabel(inNetwork(cheatBan(playerOther, 58, ban), "net-b")) // lifted in its own network first
	if l, ok := playerLabels(t, e, tenantA, playerMissed)[store.SourceLiteBansUnflagged+"/cheat"]; !ok || !l.WindowEnd.Equal(ban) {
		t.Fatalf("network A's ban #58 not applied: %v", playerLabels(t, e, tenantA, playerMissed))
	}
	if ls := playerLabels(t, e, tenantA, playerOther); len(ls) != 1 || !ls[store.SourceLiteBansUnflagged+"/legit"].WindowEnd.Equal(ban) {
		t.Fatalf("network B's unban #58: %v", ls)
	}
	// Unflagged cheat bans #59 of both networks: one label per network's entry.
	ban59 := fixedNow.Add(-5 * time.Hour)
	e.applyLabel(inNetwork(cheatBan(playerMissed, 59, ban59), "net-a"))
	e.applyLabel(inNetwork(cheatBan(playerOther, 59, ban59), "net-b"))
	for _, p := range []string{playerMissed, playerOther} {
		n := 0
		ls, _, err := e.d.Store.ExportLabels(ctx, tenantA, time.Time{}, store.Page{Limit: 200})
		check(t, err)
		for _, l := range ls {
			if l.PlayerUUID == p && l.Source == store.SourceLiteBansUnflagged && l.Label.Label == store.LabelCheat && l.WindowEnd.Equal(ban59) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%s: %d unflagged cheat labels of ban #59", p, n)
		}
	}
}

// unflaggedLabels counts the tenant's litebans_unflagged labels of player by value.
func unflaggedLabels(t *testing.T, e *env, player string) map[string]int {
	t.Helper()
	ls, _, err := e.d.Store.ExportLabels(context.Background(), tenantA, time.Time{}, store.Page{Limit: 200})
	check(t, err)
	out := map[string]int{}
	for _, l := range ls {
		if l.PlayerUUID == player && l.Source == store.SourceLiteBansUnflagged {
			out[l.Label.Label]++
		}
	}
	return out
}

// Networks that share one LiteBans database under different network ids (a misconfiguration) report
// every ban once per network id: the decisions are idempotent, and the unflagged window label of a ban
// or wrongful unban is written once, so training does not count it twice.
func TestLiteBansSharedDatabaseLabelsOnce(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	networks := []string{"net-a", "net-b"}
	for _, n := range networks {
		e.applyLabel(inNetwork(cheatBan(playerMissed, 7, ban), n))
		e.applyLabel(inNetwork(cheatBan(player1, 8, ban), n))
	}
	if got := unflaggedLabels(t, e, playerMissed); got[store.LabelCheat] != 1 || got[store.LabelLegit] != 0 {
		t.Fatalf("unflagged labels after the ban from two network ids: %v", got)
	}
	for _, n := range networks {
		row, err := e.d.Store.GetModerationLabel(ctx, tenantA, n, sourceLiteBans, "7", store.ModerationBanAdded)
		if err != nil || row.AppliedAt.IsZero() {
			t.Fatalf("%s: %+v %v", n, row, err)
		}
	}
	// Confirming twice changes nothing (playerLabels fails on a duplicate).
	if ls := playerLabels(t, e, tenantA, player1); len(ls) != 2 {
		t.Fatalf("labels %v", ls)
	}
	wantDecision(t, e, vBan, store.StatusConfirmed, store.DecidedByLiteBans)
	for _, n := range networks {
		e.applyLabel(inNetwork(wrongfulUnban(playerMissed, 7, ban), n))
	}
	if got := unflaggedLabels(t, e, playerMissed); got[store.LabelCheat] != 0 || got[store.LabelLegit] != 1 {
		t.Fatalf("unflagged labels after the unban from two network ids: %v", got)
	}
	// Interleaved: network B's ban arrives after network A's unban, then B's unban.
	e.applyLabel(inNetwork(cheatBan(playerOther, 9, ban), "net-a"))
	e.applyLabel(inNetwork(wrongfulUnban(playerOther, 9, ban), "net-a"))
	e.applyLabel(inNetwork(cheatBan(playerOther, 9, ban), "net-b"))
	e.applyLabel(inNetwork(wrongfulUnban(playerOther, 9, ban), "net-b"))
	if got := unflaggedLabels(t, e, playerOther); got[store.LabelCheat] != 0 || got[store.LabelLegit] != 1 {
		t.Fatalf("unflagged labels after the interleaved ban and unban: %v", got)
	}
	// Network B's ban arrives after network A's unban and B never sends that unban (its
	// legit-unban-reasons differ): the window stays legit, as the verdicts an unban overturned stay
	// overturned.
	ban10 := fixedNow.Add(-5 * time.Hour)
	e.applyLabel(inNetwork(cheatBan(playerOther, 10, ban10), "net-a"))
	e.applyLabel(inNetwork(wrongfulUnban(playerOther, 10, ban10), "net-a"))
	e.applyLabel(inNetwork(cheatBan(playerOther, 10, ban10), "net-b"))
	if got := unflaggedLabels(t, e, playerOther); got[store.LabelCheat] != 0 || got[store.LabelLegit] != 2 {
		t.Fatalf("unflagged labels after another network id's ban of a lifted ban: %v", got)
	}
}

// Two LiteBans databases behind one network id give one entry id to bans of different players. A
// wrongful unban only supersedes its ban when both name the same player: otherwise it is logged and
// applied to its own player as an unban whose ban was never seen, and the other player's ban stays (and
// still applies when it arrives after the unban).
func TestLiteBansUnbanOfAnotherPlayersEntry(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := context.Background()
	ban := fixedNow.Add(-time.Hour)
	e.applyLabel(cheatBan(player1, 57, ban))
	e.applyLabel(wrongfulUnban(player2, 57, ban))
	for _, id := range []string{vBan, vAlert} {
		wantDecision(t, e, id, store.StatusConfirmed, store.DecidedByLiteBans)
	}
	if ls := playerLabels(t, e, tenantA, player1); len(ls) != 2 || ls[vBan+"/cheat"].Source != store.SourceLiteBans {
		t.Fatalf("player1's labels after another player's unban: %v", ls)
	}
	if b, _ := e.d.Store.GetBan(ctx, tenantA, vBan); !b.Active {
		t.Fatal("another player's unban lifted the ban")
	}
	for _, id := range []string{vShadow, vMit} {
		wantDecision(t, e, id, store.StatusOverturned, store.DecidedByLiteBans)
	}
	ag, err := e.d.Store.ModerationAgreement(ctx, tenantA, fixedNow.Add(-24*time.Hour), fixedNow, 7*24*time.Hour)
	check(t, err)
	if len(ag.Bans) != 1 || ag.Bans[0].PlayerUUID != player1 || ag.Overturns != 1 {
		t.Fatalf("agreement %+v", ag)
	}

	// The other order: the unban of entry #58 is recorded first, then a ban #58 of another player.
	e.applyLabel(wrongfulUnban(playerOther, 58, ban))
	e.applyLabel(cheatBan(playerMissed, 58, ban))
	if got := unflaggedLabels(t, e, playerMissed); got[store.LabelCheat] != 1 {
		t.Fatalf("ban #58 not applied: %v", got)
	}
	if got := unflaggedLabels(t, e, playerOther); got[store.LabelLegit] != 1 || got[store.LabelCheat] != 0 {
		t.Fatalf("unban #58: %v", got)
	}
	var warned int
	for _, l := range strings.Split(e.logs.String(), "\n") {
		if !strings.Contains(l, "ban and wrongful unban of one entry have different players") {
			continue
		}
		warned++
		if !strings.Contains(l, `"level":"WARN"`) || !strings.Contains(l, `"tenant_id":"`+tenantA+`"`) {
			t.Errorf("warning %s", l)
		}
		for _, p := range []string{player1, player2, playerMissed, playerOther} {
			if strings.Contains(l, p) {
				t.Errorf("player id logged: %s", l)
			}
		}
	}
	if warned != 2 {
		t.Fatalf("%d warnings, want 2:\n%s", warned, e.logs.String())
	}
}

func TestModerationLabelRecords(t *testing.T) {
	rec := func(mut func(r *pb.LabelRecord)) *pb.LabelRecord {
		r := &pb.LabelRecord{Origin: &pb.Origin{TenantId: tenantA, ServerId: srv1}, Player: uuidBytes(player1),
			Label: &pb.ModerationLabel{Source: "litebans", EntryId: 12, Kind: pb.ModerationLabel_KIND_BAN_REMOVED,
				Classification: pb.ModerationLabel_CLASSIFICATION_LEGIT, BanStartUnixMs: uint64(fixedNow.UnixMilli()),
				RemovedUnixMs: uint64(fixedNow.Add(time.Hour).UnixMilli()), Executor: pb.ModerationLabel_EXECUTOR_CONSOLE}}
		if mut != nil {
			mut(r)
		}
		return r
	}
	m, err := moderationLabel(rec(nil), tenantA)
	check(t, err)
	if m.EntryID != "12" || m.Kind != store.ModerationBanRemoved || m.Label != store.LabelLegit || m.PlayerUUID != player1 ||
		!m.BanStart.Equal(fixedNow) || !m.RemovedAt.Equal(fixedNow.Add(time.Hour)) || m.Executor != store.ExecutorConsole || m.Permanent ||
		m.NetworkID != "" {
		t.Fatalf("label %+v", m)
	}
	// The entry belongs to the sending server's network.
	if m, err := moderationLabel(rec(func(r *pb.LabelRecord) { r.Origin.NetworkId = "net-a" }), tenantA); err != nil || m.NetworkID != "net-a" {
		t.Fatalf("network id: %+v %v", m, err)
	}
	for name, mut := range map[string]func(r *pb.LabelRecord){
		"other subject":   nil,
		"bad tenant":      func(r *pb.LabelRecord) { r.Origin.TenantId = "t_x.*" },
		"no player":       func(r *pb.LabelRecord) { r.Player = nil },
		"zero player":     func(r *pb.LabelRecord) { r.Player = make([]byte, 16) },
		"unknown source":  func(r *pb.LabelRecord) { r.Label.Source = "advancedban" },
		"no entry":        func(r *pb.LabelRecord) { r.Label.EntryId = 0 },
		"no start":        func(r *pb.LabelRecord) { r.Label.BanStartUnixMs = 0 },
		"cheat removal":   func(r *pb.LabelRecord) { r.Label.Classification = pb.ModerationLabel_CLASSIFICATION_CHEAT },
		"legit ban":       func(r *pb.LabelRecord) { r.Label.Kind = pb.ModerationLabel_KIND_BAN_ADDED },
		"unspecified":     func(r *pb.LabelRecord) { r.Label.Kind = pb.ModerationLabel_KIND_UNSPECIFIED },
		"no label at all": func(r *pb.LabelRecord) { r.Label = nil },
	} {
		subject := tenantA
		if name == "other subject" {
			subject = tenantB
		}
		if _, err := moderationLabel(rec(mut), subject); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The label consumer applies pe.labels.* under its lease and skips records it cannot apply.
func TestLabelConsumer(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.runLabels(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("label consumer did not stop")
		}
	}()
	publish := func(tenant string, rec *pb.LabelRecord, id string) {
		data, err := proto.Marshal(rec)
		check(t, err)
		subj, err := stream.LabelsSubject(tenant)
		check(t, err)
		_, err = e.d.Bus.Publish(context.Background(), stream.Message{Subject: subj, Data: data, MsgID: id})
		check(t, err)
	}
	label := &pb.ModerationLabel{Source: "litebans", EntryId: 4521, Kind: pb.ModerationLabel_KIND_BAN_ADDED,
		Classification: pb.ModerationLabel_CLASSIFICATION_CHEAT, BanStartUnixMs: uint64(fixedNow.Add(-time.Hour).UnixMilli()),
		Executor: pb.ModerationLabel_EXECUTOR_STAFF}
	unknown := proto.CloneOf(label)
	unknown.Source = "advancedban"
	publish(tenantA, &pb.LabelRecord{Origin: &pb.Origin{TenantId: tenantA}, Player: uuidBytes(player1), Label: unknown}, "a")
	publish(tenantB, &pb.LabelRecord{Origin: &pb.Origin{TenantId: tenantA}, Player: uuidBytes(player1), Label: label}, "b")
	publish(tenantA, &pb.LabelRecord{Origin: &pb.Origin{TenantId: tenantA, ServerId: srv1}, Player: uuidBytes(player1), Label: label}, "c")
	deadline := time.Now().Add(20 * time.Second)
	for {
		row, err := e.d.Store.GetModerationLabel(context.Background(), tenantA, "", sourceLiteBans, "4521", store.ModerationBanAdded)
		if err == nil && !row.AppliedAt.IsZero() {
			break
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the label was not applied")
		}
		time.Sleep(50 * time.Millisecond)
	}
	wantDecision(t, e, vBan, store.StatusConfirmed, store.DecidedByLiteBans)
	if _, err := e.d.Store.GetModerationLabel(context.Background(), tenantB, "", sourceLiteBans, "4521", store.ModerationBanAdded); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record on another tenant's subject was applied: %v", err)
	}
	if !strings.Contains(e.logs.String(), `unknown label source`) {
		t.Fatal("skipped record not logged")
	}
}

// GET /api/v1/stats/litebans compares the cheat bans of the last 7 and 30 days with the verdicts; the
// janitor logs it daily without player ids.
func TestLiteBansStats(t *testing.T) {
	e := newEnv(t)
	e.seed()
	day := 24 * time.Hour
	// player1: flagged (ALERT 3 h and BAN 2 h before now), banned 1 h ago: highest BAN, lead 2 h.
	e.applyLabel(cheatBan(player1, 1, fixedNow.Add(-time.Hour)))
	// playerMissed: banned 2 d ago without a verdict, and 10 d ago with a ban lifted as wrong.
	e.applyLabel(cheatBan(playerMissed, 2, fixedNow.Add(-2*day)))
	e.applyLabel(cheatBan(playerMissed, 3, fixedNow.Add(-10*day)))
	e.applyLabel(wrongfulUnban(playerMissed, 3, fixedNow.Add(-10*day)))
	// player2: a shadow BAN 4 h ago, never banned in LiteBans.

	_, key := e.apiKey(tenantA, "read")
	r := e.must(200, "GET", "/api/v1/stats/litebans", nil, bearer(key)).json(t)
	keys(t, "litebans stats", r, []string{"lookback_s", "last_7d", "last_30d"})
	if r["lookback_s"] != float64(7*24*3600) {
		t.Fatalf("lookback %v", r["lookback_s"])
	}
	want := map[string]any{"cheat_bans": 2.0, "players": 2.0, "not_flagged": 1.0, "median_lead_time_s": 7200.0,
		"would_kick_or_ban": 2.0, "would_kick_or_ban_banned": 1.0, "overturns": 1.0, "verdicts_overturned": 0.0}
	for _, w := range []string{"last_7d", "last_30d"} {
		got := r[w].(map[string]any)
		keys(t, w, got, []string{"cheat_bans", "players", "flagged_before", "not_flagged", "median_lead_time_s", "would_kick_or_ban",
			"would_kick_or_ban_banned", "overturns", "verdicts_overturned"})
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s %s = %v, want %v", w, k, got[k], v)
			}
		}
		fb, _ := json.Marshal(got["flagged_before"])
		if string(fb) != `{"alert":0,"ban":1,"kick":0,"mitigate":0}` {
			t.Errorf("%s flagged_before %s", w, fb)
		}
	}
	other := e.must(200, "GET", "/api/v1/stats/litebans", nil, as(tenantB, "viewer")).json(t)["last_30d"].(map[string]any)
	if other["cheat_bans"] != 0.0 || other["median_lead_time_s"] != nil || other["would_kick_or_ban"] != 1.0 {
		t.Fatalf("tenant B %v", other)
	}
	if r := e.do("GET", "/api/v1/stats/litebans", nil); r.status != 401 {
		t.Fatalf("unauthenticated: %d", r.status)
	}

	check(t, e.s.janitorOnce(context.Background(), fixedNow))
	var lines []string
	for _, l := range strings.Split(e.logs.String(), "\n") {
		if strings.Contains(l, `"msg":"litebans agreement"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("agreement log lines %q", lines)
	}
	var line struct {
		Level     string `json:"level"`
		Tenant    string `json:"tenant_id"`
		Window    string `json:"window"`
		CheatBans int    `json:"cheat_bans"`
		Flagged   int    `json:"flagged_ban"`
		Lead      int64  `json:"median_lead_time_s"`
	}
	check(t, json.Unmarshal([]byte(lines[1]), &line))
	if line.Level != "INFO" || line.Tenant != tenantA || line.Window != "30d" || line.CheatBans != 2 || line.Flagged != 1 || line.Lead != 7200 {
		t.Fatalf("log line %s", lines[1])
	}
	for _, l := range lines {
		for _, p := range []string{player1, player2, playerMissed} {
			if strings.Contains(l, p) {
				t.Fatalf("player id logged: %s", l)
			}
		}
	}
}
