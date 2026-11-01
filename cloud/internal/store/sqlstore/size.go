package sqlstore

import "context"

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
