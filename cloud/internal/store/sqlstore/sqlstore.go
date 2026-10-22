// Package sqlstore implements store.Store with database/sql for postgres (pgx stdlib) and sqlite
// (modernc.org/sqlite: WAL, busy_timeout 5000, foreign keys, one writer connection and a separate
// reader pool). Both dialects share every query; differences (placeholders, column types, a few
// functions) are isolated in dialect. Times are stored as unix microseconds.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" driver
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Drivers accepted by Open (PE_DB_DRIVER).
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// Store is the database/sql implementation of store.Store.
type Store struct {
	d dialect
	w *sql.DB // writes and transactions (sqlite: exactly one connection)
	r *sql.DB // reads (postgres: the same pool as w)
}

var _ store.Store = (*Store)(nil)

// Options tunes Open.
type Options struct {
	ReadConns int // sqlite reader pool size (default 4)
	MaxConns  int // postgres pool size (default 20)
}

// Open connects to the database. For sqlite, dsn is a file path (created with its directory) or a
// "file:" URI; the required pragmas are added. Open does not migrate; call Migrate or CheckSchema.
func Open(ctx context.Context, driver, dsn string, opts Options) (*Store, error) {
	if opts.ReadConns <= 0 {
		opts.ReadConns = 4
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = 20
	}
	var s *Store
	switch driver {
	case DriverSQLite:
		w, r, err := openSQLite(dsn, opts.ReadConns)
		if err != nil {
			return nil, err
		}
		s = &Store{d: dialectSQLite, w: w, r: r}
	case DriverPostgres:
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: %w", err)
		}
		db.SetMaxOpenConns(opts.MaxConns)
		db.SetMaxIdleConns(opts.MaxConns)
		db.SetConnMaxIdleTime(5 * time.Minute)
		s = &Store{d: dialectPostgres, w: db, r: db}
	default:
		return nil, fmt.Errorf("sqlstore: unknown driver %q", driver)
	}
	if err := s.Ping(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("sqlstore: connect: %w", err)
	}
	return s, nil
}

func openSQLite(dsn string, readers int) (w, r *sql.DB, err error) {
	if !strings.HasPrefix(dsn, "file:") {
		if dir := filepath.Dir(dsn); dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, nil, fmt.Errorf("sqlstore: %w", err)
			}
		}
		dsn = "file:" + (&url.URL{Path: dsn}).EscapedPath()
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	base := dsn + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	w, err = sql.Open("sqlite", base+"&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, nil, fmt.Errorf("sqlstore: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	// The writer creates the file and switches it to WAL before any reader opens it.
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, nil, fmt.Errorf("sqlstore: %w", err)
	}
	r, err = sql.Open("sqlite", base+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, nil, fmt.Errorf("sqlstore: %w", err)
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	return w, r, nil
}

// Ping checks both pools.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.w.PingContext(ctx); err != nil {
		return err
	}
	return s.r.PingContext(ctx)
}

// Close closes the pools.
func (s *Store) Close() error {
	err := s.w.Close()
	if s.r != s.w {
		err = errors.Join(err, s.r.Close())
	}
	return err
}

// ErrBackupUnsupported is returned by Backup for postgres, which has its own tooling (pg_dump).
var ErrBackupUnsupported = errors.New("sqlstore: online backup is only implemented for sqlite")

// Backup writes a consistent copy of a sqlite database to path (which must not exist) while the
// store stays in use. It runs on the writer connection, so writes wait for it.
func (s *Store) Backup(ctx context.Context, path string) error {
	if s.d != dialectSQLite {
		return ErrBackupUnsupported
	}
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("sqlstore: backup: %w", err)
	}
	return nil
}

// runner is what both *sql.DB and *sql.Tx offer.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rebind turns ? placeholders into $1, $2... for postgres. Queries never contain a literal '?'.
func (s *Store) rebind(q string) string {
	if s.d != dialectPostgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, r runner, q string, args ...any) (sql.Result, error) {
	res, err := r.ExecContext(ctx, s.rebind(q), args...)
	return res, mapErr(err)
}

func (s *Store) query(ctx context.Context, r runner, q string, args ...any) (*sql.Rows, error) {
	rows, err := r.QueryContext(ctx, s.rebind(q), args...)
	return rows, mapErr(err)
}

func (s *Store) queryRow(ctx context.Context, r runner, q string, args ...any) *sql.Row {
	return r.QueryRowContext(ctx, s.rebind(q), args...)
}

// tx runs fn in a write transaction, committing when fn returns nil.
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return mapErr(tx.Commit())
}

// greatest returns the SQL for the larger of two expressions.
func (s *Store) greatest(a, b string) string {
	if s.d == dialectPostgres {
		return "GREATEST(" + a + ", " + b + ")"
	}
	return "max(" + a + ", " + b + ")"
}

// forUpdate locks selected rows in postgres; sqlite serialises writers anyway.
func (s *Store) forUpdate() string {
	if s.d == dialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

// mapErr maps driver errors to store errors: unique violations to ErrConflict, foreign key
// violations (missing tenant) to ErrNotFound, sql.ErrNoRows to ErrNotFound.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return fmt.Errorf("%w: %s", store.ErrConflict, pg.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: %s", store.ErrNotFound, pg.ConstraintName)
		}
		return err
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
			return fmt.Errorf("%w: %v", store.ErrConflict, se)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%w: %v", store.ErrNotFound, se)
		}
	}
	return err
}

func now() time.Time { return time.Now().UTC() }

// orNow returns t, or the current time when t is zero.
func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return now()
	}
	return t
}

// us converts a time to a nullable column value (zero = NULL).
func us(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMicro()
}

// fromUS converts a nullable microsecond column.
func fromUS(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.UnixMicro(n.Int64).UTC()
}

// placeholders returns "?, ?, ..." for n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// pageArgs returns the keyset condition for newest-first lists ordered by (col, id) and its args.
func pageCond(page store.Page, col, idCol string) (string, []any, error) {
	if page.Cursor == "" {
		return "", nil, nil
	}
	t, id, err := store.DecodeCursor(page.Cursor)
	if err != nil {
		return "", nil, err
	}
	c := t.UnixMicro()
	return " AND (" + col + " < ? OR (" + col + " = ? AND " + idCol + " < ?))", []any{c, c, id}, nil
}

// pageCondAsc is pageCond for oldest-first lists.
func pageCondAsc(page store.Page, col, idCol string) (string, []any, error) {
	if page.Cursor == "" {
		return "", nil, nil
	}
	t, id, err := store.DecodeCursor(page.Cursor)
	if err != nil {
		return "", nil, err
	}
	c := t.UnixMicro()
	return " AND (" + col + " > ? OR (" + col + " = ? AND " + idCol + " > ?))", []any{c, c, id}, nil
}

// nextCursor returns the cursor after the last item when the page is full.
func nextCursor(n, size int, last func() (time.Time, string)) string {
	if n < size {
		return ""
	}
	t, id := last()
	return store.EncodeCursor(t, id)
}
