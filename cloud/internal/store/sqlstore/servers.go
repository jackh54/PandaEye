package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

const serverCols = `tenant_id, id, name, network_id, platform, platform_version, plugin_version, minecraft_version,
	secret_hash, instance_id, last_seen_at, last_batch_seq, created_at`

func scanServer(sc interface{ Scan(...any) error }) (*store.Server, error) {
	var v store.Server
	var platform, seq int64
	var seen, created sql.NullInt64
	if err := sc.Scan(&v.TenantID, &v.ID, &v.Name, &v.NetworkID, &platform, &v.PlatformVersion, &v.PluginVersion,
		&v.MinecraftVersion, &v.SecretHash, &v.InstanceID, &seen, &seq, &created); err != nil {
		return nil, mapErr(err)
	}
	v.Platform, v.LastBatchSeq = pb.Platform(platform), uint64(seq)
	v.LastSeenAt, v.CreatedAt = fromUS(seen), fromUS(created)
	return &v, nil
}

func (s *Store) CreateServer(ctx context.Context, v *store.Server) error {
	v.CreatedAt = orNow(v.CreatedAt)
	_, err := s.exec(ctx, s.w, `INSERT INTO servers (`+serverCols+`) VALUES (`+placeholders(13)+`)`,
		v.TenantID, v.ID, v.Name, v.NetworkID, int64(v.Platform), v.PlatformVersion, v.PluginVersion, v.MinecraftVersion,
		v.SecretHash, v.InstanceID, us(v.LastSeenAt), int64(v.LastBatchSeq), v.CreatedAt.UnixMicro())
	return err
}

func (s *Store) GetServer(ctx context.Context, tenantID, serverID string) (*store.Server, error) {
	return scanServer(s.queryRow(ctx, s.r, `SELECT `+serverCols+` FROM servers WHERE tenant_id = ? AND id = ?`, tenantID, serverID))
}

func (s *Store) UpdateServerSeen(ctx context.Context, v *store.Server) error {
	return s.updateOne(ctx, `UPDATE servers SET name = ?, network_id = ?, platform = ?, platform_version = ?, plugin_version = ?,
		minecraft_version = ?, instance_id = ?, last_seen_at = ? WHERE tenant_id = ? AND id = ?`,
		v.Name, v.NetworkID, int64(v.Platform), v.PlatformVersion, v.PluginVersion, v.MinecraftVersion, v.InstanceID,
		orNow(v.LastSeenAt).UnixMicro(), v.TenantID, v.ID)
}

func (s *Store) AdvanceServerSeq(ctx context.Context, tenantID, serverID string, seq uint64, at time.Time) error {
	return s.updateOne(ctx, `UPDATE servers SET last_batch_seq = `+s.greatest("last_batch_seq", "?")+`, last_seen_at = ?
		WHERE tenant_id = ? AND id = ?`, int64(seq), orNow(at).UnixMicro(), tenantID, serverID)
}

func (s *Store) ListServers(ctx context.Context, tenantID string, page store.Page) ([]store.Server, string, error) {
	cond, args, err := pageCond(page, "created_at", "id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT `+serverCols+` FROM servers WHERE tenant_id = ?`+cond+
		` ORDER BY created_at DESC, id DESC LIMIT ?`, append(append([]any{tenantID}, args...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Server
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.ID }), nil
}
