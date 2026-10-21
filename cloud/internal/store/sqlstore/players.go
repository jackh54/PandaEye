package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
)

func (s *Store) UpsertPlayer(ctx context.Context, tenantID, player, name, serverID string, at time.Time) error {
	t := orNow(at).UnixMicro()
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.exec(ctx, tx, `INSERT INTO players (tenant_id, uuid, last_name, first_seen_at, last_seen_at, last_server_id)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (tenant_id, uuid) DO UPDATE SET
				last_name = CASE WHEN excluded.last_seen_at >= players.last_seen_at THEN excluded.last_name ELSE players.last_name END,
				last_server_id = CASE WHEN excluded.last_seen_at >= players.last_seen_at THEN excluded.last_server_id ELSE players.last_server_id END,
				last_seen_at = `+s.greatest("players.last_seen_at", "excluded.last_seen_at"),
			tenantID, player, name, t, t, serverID); err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		_, err := s.exec(ctx, tx, `INSERT INTO player_names (tenant_id, uuid, name, seen_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (tenant_id, uuid, name) DO UPDATE SET seen_at = `+s.greatest("player_names.seen_at", "excluded.seen_at"),
			tenantID, player, name, t)
		return err
	})
}

const playerCols = `uuid, last_name, first_seen_at, last_seen_at, last_server_id`

func scanPlayer(tenantID string, sc interface{ Scan(...any) error }) (*store.Player, error) {
	p := store.Player{TenantID: tenantID}
	var first, last sql.NullInt64
	if err := sc.Scan(&p.UUID, &p.LastName, &first, &last, &p.LastServerID); err != nil {
		return nil, mapErr(err)
	}
	p.FirstSeenAt, p.LastSeenAt = fromUS(first), fromUS(last)
	return &p, nil
}

func (s *Store) GetPlayer(ctx context.Context, tenantID, player string) (*store.Player, error) {
	p, err := scanPlayer(tenantID, s.queryRow(ctx, s.r, `SELECT `+playerCols+` FROM players WHERE tenant_id = ? AND uuid = ?`, tenantID, player))
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, s.r, `SELECT name, seen_at FROM player_names WHERE tenant_id = ? AND uuid = ? ORDER BY seen_at DESC, name`, tenantID, player)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n store.PlayerName
		var seen sql.NullInt64
		if err := rows.Scan(&n.Name, &seen); err != nil {
			return nil, err
		}
		n.SeenAt = fromUS(seen)
		p.Names = append(p.Names, n)
	}
	return p, rows.Err()
}

func (s *Store) FindPlayersByName(ctx context.Context, tenantID, prefix string, limit int) ([]store.Player, error) {
	limit = store.Page{Limit: limit}.Size()
	pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(prefix)) + "%"
	rows, err := s.query(ctx, s.r, `SELECT `+playerCols+` FROM players p WHERE p.tenant_id = ? AND EXISTS (
			SELECT 1 FROM player_names n WHERE n.tenant_id = p.tenant_id AND n.uuid = p.uuid AND lower(n.name) LIKE ? ESCAPE '\')
		ORDER BY p.last_seen_at DESC, p.uuid LIMIT ?`, tenantID, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Player
	for rows.Next() {
		p, err := scanPlayer(tenantID, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
