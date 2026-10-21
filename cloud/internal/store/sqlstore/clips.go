package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) SaveClip(ctx context.Context, c *store.Clip) error {
	c.CreatedAt = orNow(c.CreatedAt)
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `INSERT INTO clips (id, tenant_id, server_id, player_uuid, start_us, end_us, codec, data, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.TenantID, c.ServerID, c.PlayerUUID, c.StartUs, c.EndUs, c.Codec, c.Data,
			c.CreatedAt.UnixMicro()); err != nil {
			return err
		}
		for _, p := range append([]string{c.PlayerUUID}, c.Subjects...) {
			if _, err := s.exec(ctx, tx, `INSERT INTO clip_subjects (clip_id, player_uuid, tenant_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
				c.ID, p, c.TenantID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetClip(ctx context.Context, tenantID, id string) (*store.Clip, error) {
	c := store.Clip{TenantID: tenantID}
	var created int64
	if err := s.queryRow(ctx, s.r, `SELECT id, server_id, player_uuid, start_us, end_us, codec, data, created_at FROM clips
		WHERE id = ? AND tenant_id = ?`, id, tenantID).Scan(&c.ID, &c.ServerID, &c.PlayerUUID, &c.StartUs, &c.EndUs, &c.Codec,
		&c.Data, &created); err != nil {
		return nil, mapErr(err)
	}
	c.CreatedAt = time.UnixMicro(created).UTC()
	rows, err := s.query(ctx, s.r, `SELECT player_uuid FROM clip_subjects WHERE clip_id = ? AND tenant_id = ? AND player_uuid <> ?
		ORDER BY player_uuid`, id, tenantID, c.PlayerUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		c.Subjects = append(c.Subjects, p)
	}
	return &c, rows.Err()
}

func (s *Store) DeleteClip(ctx context.Context, tenantID, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		n, err := s.deleteClips(ctx, tx, tenantID, `id = ?`, id)
		if err == nil && n == 0 {
			return store.ErrNotFound
		}
		return err
	})
}

// deleteClips deletes the tenant's clips matching cond together with their verdict links (subjects
// cascade).
func (s *Store) deleteClips(ctx context.Context, tx *sql.Tx, tenantID, cond string, args ...any) (int64, error) {
	all := append([]any{tenantID, tenantID}, args...)
	if _, err := s.exec(ctx, tx, `DELETE FROM verdict_clips WHERE tenant_id = ? AND clip_id IN (SELECT id FROM clips WHERE tenant_id = ? AND `+cond+`)`, all...); err != nil {
		return 0, err
	}
	res, err := s.exec(ctx, tx, `DELETE FROM clips WHERE tenant_id = ? AND `+cond, append([]any{tenantID}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) PurgeClips(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	var n int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = s.deleteClips(ctx, tx, tenantID, `created_at < ? AND id NOT IN (
			SELECT vc.clip_id FROM verdict_clips vc WHERE vc.tenant_id = ? AND (
				EXISTS (SELECT 1 FROM bans b WHERE b.tenant_id = vc.tenant_id AND b.verdict_id = vc.verdict_id AND b.active = ?)
				OR EXISTS (SELECT 1 FROM appeals a WHERE a.tenant_id = vc.tenant_id AND a.verdict_id = vc.verdict_id AND a.status = ?)
				OR EXISTS (SELECT 1 FROM labels l WHERE l.tenant_id = vc.tenant_id AND l.verdict_id = vc.verdict_id)))`,
			before.UnixMicro(), tenantID, true, store.AppealOpen)
		return err
	})
	return n, err
}
