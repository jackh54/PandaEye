package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations
var migrationFS embed.FS

// ErrSchemaOutdated is returned by CheckSchema when migrations are pending.
var ErrSchemaOutdated = errors.New("sqlstore: database schema is outdated; run `pandaeye migrate`")

type migration struct {
	version int
	name    string
	sql     string
}

func (s *Store) migrations() ([]migration, error) {
	dir := "migrations/sqlite"
	if s.d == dialectPostgres {
		dir = "migrations/postgres"
	}
	entries, err := fs.ReadDir(migrationFS, dir)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("sqlstore: bad migration file name %q", e.Name())
		}
		b, err := migrationFS.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// LatestVersion returns the highest embedded migration version.
func (s *Store) LatestVersion() (int, error) {
	ms, err := s.migrations()
	if err != nil || len(ms) == 0 {
		return 0, err
	}
	return ms[len(ms)-1].version, nil
}

// SchemaVersion returns the highest applied migration (0 for an empty database).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.w.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v)
	if err != nil {
		if s.tableMissing(err) {
			return 0, nil
		}
		return 0, err
	}
	return int(v.Int64), nil
}

func (s *Store) tableMissing(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist")
}

// CheckSchema returns ErrSchemaOutdated unless every embedded migration is applied.
func (s *Store) CheckSchema(ctx context.Context) error {
	have, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	want, err := s.LatestVersion()
	if err != nil {
		return err
	}
	if have < want {
		return fmt.Errorf("%w (have %d, want %d)", ErrSchemaOutdated, have, want)
	}
	return nil
}

// Migrate applies pending migrations, each in its own transaction, and returns how many it applied.
// Concurrent migrators are serialised (postgres advisory lock; sqlite's single writer).
func (s *Store) Migrate(ctx context.Context) (int, error) {
	ms, err := s.migrations()
	if err != nil {
		return 0, err
	}
	create := "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)"
	if s.d == dialectPostgres {
		create = "CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, applied_at BIGINT NOT NULL)"
	}
	if _, err := s.w.ExecContext(ctx, create); err != nil {
		return 0, fmt.Errorf("sqlstore: create schema_migrations: %w", err)
	}
	applied := 0
	for _, m := range ms {
		done := false
		err := s.tx(ctx, func(tx *sql.Tx) error {
			if s.d == dialectPostgres {
				if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7065646179)"); err != nil {
					return err
				}
			}
			var n int
			if err := s.queryRow(ctx, tx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				done = true
				return nil
			}
			for _, stmt := range splitStatements(m.sql) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w\n%s", m.name, err, stmt)
				}
			}
			_, err := s.exec(ctx, tx, "INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", m.version, now().UnixMicro())
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("sqlstore: migrate: %w", err)
		}
		if !done {
			applied++
		}
	}
	return applied, nil
}

// splitStatements splits a migration file at semicolons that end a line; comment lines are dropped.
func splitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}
