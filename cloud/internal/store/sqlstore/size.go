package sqlstore

import (
	"context"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

// Size returns the size of the database in bytes: page_count * page_size for sqlite (an online
// backup needs about as much), pg_database_size for postgres.
func (s *Store) Size(ctx context.Context) (int64, error) {
	q := `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`
	if s.d == dialectPostgres {
		q = `SELECT pg_database_size(current_database())`
	}
	var n int64
	err := s.r.QueryRowContext(ctx, q).Scan(&n)
	return n, mapErr(err)
}

// SizeReport returns the database size and the rows and bytes of every table (the janitor logs it
// daily). Table bytes include the indexes (sqlite: the dbstat table, left out if the library lacks
// it; postgres: pg_total_relation_size).
func (s *Store) SizeReport(ctx context.Context) (store.SizeReport, error) {
	var r store.SizeReport
	var err error
	if r.Bytes, err = s.Size(ctx); err != nil {
		return r, err
	}
	tables := `SELECT name, 0 FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
	if s.d == dialectPostgres {
		tables = `SELECT tablename, 0 FROM pg_tables WHERE schemaname = current_schema()`
	}
	if r.Rows, err = s.named(ctx, tables); err != nil {
		return r, err
	}
	for t := range r.Rows {
		var n int64
		if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(t, `"`, `""`)+`"`).Scan(&n); err != nil {
			return r, mapErr(err)
		}
		r.Rows[t] = n
	}
	if s.d == dialectPostgres {
		r.TableBytes, err = s.named(ctx, `SELECT c.relname, pg_total_relation_size(c.oid) FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relkind = 'r'`)
		return r, err
	}
	if err := s.r.QueryRowContext(ctx, `SELECT freelist_count * page_size FROM pragma_freelist_count(), pragma_page_size()`).Scan(&r.FreeBytes); err != nil {
		return r, mapErr(err)
	}
	r.TableBytes, _ = s.named(ctx, `SELECT m.tbl_name, sum(d.pgsize) FROM dbstat d JOIN sqlite_master m ON m.name = d.name GROUP BY m.tbl_name`)
	return r, nil
}

// named runs q, which returns (name, number) rows, into a map.
func (s *Store) named(ctx context.Context, q string) (map[string]int64, error) {
	rows, err := s.query(ctx, s.r, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, mapErr(rows.Err())
}
