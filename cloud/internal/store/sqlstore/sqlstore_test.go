package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
