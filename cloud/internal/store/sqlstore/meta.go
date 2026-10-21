package sqlstore

import "context"

func (s *Store) GetMeta(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.queryRow(ctx, s.r, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v, mapErr(err)
}

func (s *Store) PutMetaIfAbsent(ctx context.Context, key string, value []byte) ([]byte, error) {
	if _, err := s.exec(ctx, s.w, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, value); err != nil {
		return nil, err
	}
	var v []byte
	if err := s.queryRow(ctx, s.w, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil {
		return nil, mapErr(err)
	}
	return v, nil
}
