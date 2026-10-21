package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// TestAPIStore covers store.APIStore on sqlite and, with PE_TEST_POSTGRES_DSN, on postgres.
func TestAPIStore(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testAPIStore(t, openSQLiteTest(t)) })
	dsn := os.Getenv("PE_TEST_POSTGRES_DSN")
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		schema := "pe_test_" + strings.ToLower(identity.NewTenantID()[2:])
		if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		s, err := Open(context.Background(), DriverPostgres, withSearchPath(dsn, schema), Options{MaxConns: 4})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		testAPIStore(t, s)
	})
}

func testAPIStore(t *testing.T, s *Store) {
	ctx := context.Background()
	t0 := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := identity.NewTenantID(), identity.NewTenantID()
	for _, id := range []string{a, b} {
		must(s.CreateTenant(ctx, store.NewTenant(id, id, "slug-"+id[2:], t0)))
	}
	server := identity.NewUUID().String()
	var ids []string
	for i, shadow := range []bool{true, false, true, false} {
		v := &store.Verdict{ID: identity.NewUUID().String(), TenantID: a, ServerID: server, PlayerUUID: identity.NewUUID().String(),
			Action: pb.Action_ACTION_BAN, Category: pb.Category(i + 1), Shadow: shadow, CreatedAt: t0.Add(time.Duration(i) * time.Minute)}
		if _, err := s.UpsertVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	other := &store.Verdict{ID: identity.NewUUID().String(), TenantID: b, ServerID: server, PlayerUUID: identity.NewUUID().String(),
		Action: pb.Action_ACTION_ALERT, Shadow: true, CreatedAt: t0}
	if _, err := s.UpsertVerdict(ctx, other); err != nil {
		t.Fatal(err)
	}

	yes, no := true, false
	for _, c := range []struct {
		shadow *bool
		want   int
	}{{nil, 4}, {&yes, 2}, {&no, 2}} {
		vs, _, err := s.ListVerdictsShadow(ctx, a, store.VerdictFilter{}, c.shadow, store.Page{})
		must(err)
		if len(vs) != c.want {
			t.Fatalf("shadow %v: %d verdicts", c.shadow, len(vs))
		}
	}
	page1, cur, err := s.ListVerdictsShadow(ctx, a, store.VerdictFilter{Action: pb.Action_ACTION_BAN}, &yes, store.Page{Limit: 1})
	must(err)
	page2, cur2, err := s.ListVerdictsShadow(ctx, a, store.VerdictFilter{Action: pb.Action_ACTION_BAN}, &yes, store.Page{Limit: 1, Cursor: cur})
	must(err)
	if len(page1) != 1 || len(page2) != 1 || page1[0].ID == page2[0].ID || page1[0].ID != ids[2] {
		t.Fatalf("pages %v %v", page1, page2)
	}
	if last, _, _ := s.ListVerdictsShadow(ctx, a, store.VerdictFilter{}, &yes, store.Page{Limit: 1, Cursor: cur2}); len(last) != 0 {
		t.Fatal("past the end")
	}
	if n, err := s.CountShadowVerdicts(ctx, a, t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("shadow count since t0+1m: %d %v", n, err)
	}

	if tn, err := s.LookupVerdictTenant(ctx, other.ID); err != nil || tn != b {
		t.Fatalf("lookup: %q %v", tn, err)
	}
	if _, err := s.LookupVerdictTenant(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown verdict: %v", err)
	}
	cats, err := s.VerdictCategories(ctx, a, append(append([]string{}, ids...), other.ID, "missing"))
	must(err)
	if len(cats) != 4 || cats[ids[1]] != pb.Category(2) {
		t.Fatalf("categories %v (other tenant's ids must be absent)", cats)
	}

	p1, p2 := identity.NewUUID().String(), identity.NewUUID().String()
	must(s.UpsertPlayer(ctx, a, p1, "Steve_Old", server, t0))
	must(s.UpsertPlayer(ctx, a, p1, "Steve", server, t0.Add(2*time.Hour)))
	must(s.UpsertPlayer(ctx, a, p2, "Alex", server, t0.Add(time.Hour)))
	must(s.UpsertPlayer(ctx, b, identity.NewUUID().String(), "Steve", server, t0))
	ps, _, err := s.ListPlayers(ctx, a, "", store.Page{})
	must(err)
	if len(ps) != 2 || ps[0].UUID != p1 || ps[0].LastName != "Steve" {
		t.Fatalf("players %+v", ps)
	}
	for prefix, want := range map[string]int{"steve_o": 1, "STEVE": 1, "al": 1, "x": 0, "%": 0, "_": 0} {
		got, _, err := s.ListPlayers(ctx, a, prefix, store.Page{})
		must(err)
		if len(got) != want {
			t.Errorf("prefix %q: %d players", prefix, len(got))
		}
	}
	first, cur, err := s.ListPlayers(ctx, a, "", store.Page{Limit: 1})
	must(err)
	second, _, err := s.ListPlayers(ctx, a, "", store.Page{Limit: 1, Cursor: cur})
	must(err)
	if len(first) != 1 || len(second) != 1 || second[0].UUID != p2 {
		t.Fatalf("player pages %v %v", first, second)
	}

	for _, l := range []string{store.LabelCheat, store.LabelLegit} {
		must(s.InsertLabel(ctx, &store.Label{TenantID: a, PlayerUUID: p1, VerdictID: ids[0], Label: l, Source: store.SourceManual,
			WindowStart: t0, WindowEnd: t0, CreatedAt: t0}))
	}
	must(s.InsertLabel(ctx, &store.Label{TenantID: b, PlayerUUID: p1, VerdictID: ids[0], Label: store.LabelCheat, Source: store.SourceManual,
		WindowStart: t0, WindowEnd: t0, CreatedAt: t0}))
	if n, err := s.DeleteVerdictLabels(ctx, a, ids[0], store.LabelCheat); err != nil || n != 1 {
		t.Fatalf("delete labels: %d %v", n, err)
	}
	ls, _, err := s.ExportLabels(ctx, a, time.Time{}, store.Page{})
	must(err)
	if len(ls) != 1 || ls[0].Label.Label != store.LabelLegit {
		t.Fatalf("labels left %+v", ls)
	}
	if lb, _, _ := s.ExportLabels(ctx, b, time.Time{}, store.Page{}); len(lb) != 1 {
		t.Fatal("another tenant's label deleted")
	}
}
