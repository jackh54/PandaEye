package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/store/storetest"
)

func openSQLiteTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), DriverSQLite, filepath.Join(t.TempDir(), "sub", "pe.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSQLite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return openSQLiteTest(t) })
}

// TestPostgres runs the suite against PE_TEST_POSTGRES_DSN, one fresh schema per subtest.
func TestPostgres(t *testing.T) {
	dsn := os.Getenv("PE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("PE_TEST_POSTGRES_DSN not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	storetest.Run(t, func(t *testing.T) store.Store {
		schema := "pe_test_" + strings.ToLower(identity.NewTenantID()[2:])
		if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
		s, err := Open(context.Background(), DriverPostgres, withSearchPath(dsn, schema), Options{MaxConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if _, err := s.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func withSearchPath(dsn, schema string) string {
	if strings.Contains(dsn, "://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pe.db")
	s, err := Open(ctx, DriverSQLite, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CheckSchema(ctx); !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("empty database: %v", err)
	}
	n, err := s.Migrate(ctx)
	if err != nil || n == 0 {
		t.Fatalf("migrate: %d %v", n, err)
	}
	if n, err := s.Migrate(ctx); err != nil || n != 0 {
		t.Fatalf("second migrate: %d %v", n, err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	v, _ := s.SchemaVersion(ctx)
	latest, _ := s.LatestVersion()
	if v != latest || v < 1 {
		t.Fatalf("version %d latest %d", v, latest)
	}
	for _, d := range []dialect{dialectSQLite, dialectPostgres} {
		ms, err := (&Store{d: d}).migrations()
		if err != nil || len(ms) != latest {
			t.Fatalf("dialect %d migrations: %d %v", d, len(ms), err)
		}
	}
	var mode string
	if err := s.w.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q %v", mode, err)
	}
	var fk int
	if err := s.r.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("reader foreign_keys %d %v", fk, err)
	}
	if _, err := s.r.Exec("INSERT INTO meta (key, value) VALUES ('x', 'y')"); err == nil {
		t.Fatal("reader pool must be read-only")
	}
}

// Migration 2 marks the verdicts staff confirmed or overturned before it as decided by staff, so no
// LiteBans label replaces those decisions.
func TestMigrationMarksStaffDecisions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, DriverSQLite, filepath.Join(t.TempDir(), "pe.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ms, err := s.migrations()
	if err != nil || len(ms) < 2 || ms[1].version != 2 {
		t.Fatalf("migrations %v %v", len(ms), err)
	}
	v1 := append(splitStatements(ms[0].sql), "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)",
		"INSERT INTO schema_migrations (version, applied_at) VALUES (1, 0)",
		"INSERT INTO tenants (id, name, slug, created_at, updated_at) VALUES ('t_aaaaaaaaaaaaaaaaaaaa', 'A', 'a', 0, 0)")
	for _, q := range v1 {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v: %s", err, q)
		}
	}
	for i, st := range []string{store.StatusOpen, store.StatusConfirmed, store.StatusOverturned, store.StatusExpired} {
		if _, err := s.w.ExecContext(ctx, `INSERT INTO verdicts (id, tenant_id, server_id, player_uuid, action, status, created_at)
			VALUES (?, 't_aaaaaaaaaaaaaaaaaaaa', 's', 'p', 1, ?, ?)`, st, st, i); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Migrate(ctx); err != nil || n != len(ms)-1 {
		t.Fatalf("migrate: %d %v", n, err)
	}
	want := map[string]string{store.StatusOpen: "", store.StatusConfirmed: store.DecidedByStaff, store.StatusOverturned: store.DecidedByStaff,
		store.StatusExpired: ""}
	for id, by := range want {
		v, err := s.GetVerdict(ctx, "t_aaaaaaaaaaaaaaaaaaaa", id)
		if err != nil || v.DecidedBy != by {
			t.Errorf("%s: decided by %q, want %q (%v)", id, v.DecidedBy, by, err)
		}
	}
	// A LiteBans entry is keyed by network: the same entry id of two networks is two rows.
	for _, network := range []string{"", "net-b"} {
		m := &store.ModerationLabel{TenantID: "t_aaaaaaaaaaaaaaaaaaaa", NetworkID: network, Source: "litebans", EntryID: "57",
			Kind: store.ModerationBanAdded, Label: store.LabelCheat, PlayerUUID: "p", BanStart: time.UnixMicro(1)}
		if _, created, err := s.RecordModerationLabel(ctx, m); err != nil || !created {
			t.Fatalf("entry of network %q: %v %v", network, created, err)
		}
	}
}

func TestRebind(t *testing.T) {
	pg := &Store{d: dialectPostgres}
	if got := pg.rebind("a = ? AND b IN (?, ?)"); got != "a = $1 AND b IN ($2, $3)" {
		t.Fatal(got)
	}
	if got := (&Store{d: dialectSQLite}).rebind("a = ?"); got != "a = ?" {
		t.Fatal(got)
	}
	if got := splitStatements("-- c\nCREATE TABLE a (x INT);\n\nCREATE INDEX i ON a (x);\n"); len(got) != 2 {
		t.Fatalf("split %q", got)
	}
}
