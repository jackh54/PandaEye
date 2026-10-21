package sqlstore

import (
	"context"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Queries of store.APIStore (the REST API).

func (s *Store) ListVerdictsShadow(ctx context.Context, tenantID string, f store.VerdictFilter, shadow *bool, page store.Page) ([]store.Verdict, string, error) {
	where := " WHERE tenant_id = ?"
	args := []any{tenantID}
	add := func(cond string, v any) { where += " AND " + cond; args = append(args, v) }
	if f.Action != pb.Action_ACTION_UNSPECIFIED {
		add("action = ?", int64(f.Action))
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.PlayerUUID != "" {
		add("player_uuid = ?", f.PlayerUUID)
	}
	if f.ServerID != "" {
		add("server_id = ?", f.ServerID)
	}
	if !f.Since.IsZero() {
		add("created_at >= ?", f.Since.UnixMicro())
	}
	if !f.Until.IsZero() {
		add("created_at < ?", f.Until.UnixMicro())
	}
	if shadow != nil {
		add("shadow = ?", *shadow)
	}
	cond, cargs, err := pageCond(page, "created_at", "id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT `+verdictCols+` FROM verdicts`+where+cond+` ORDER BY created_at DESC, id DESC LIMIT ?`,
		append(append(args, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	out, err := s.scanVerdicts(rows)
	if err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.ID }), nil
}

func (s *Store) CountShadowVerdicts(ctx context.Context, tenantID string, since time.Time) (int64, error) {
	var n int64
	err := s.queryRow(ctx, s.r, `SELECT COUNT(*) FROM verdicts WHERE tenant_id = ? AND created_at >= ? AND shadow = ?`,
		tenantID, since.UnixMicro(), true).Scan(&n)
	return n, mapErr(err)
}

func (s *Store) ListPlayers(ctx context.Context, tenantID, namePrefix string, page store.Page) ([]store.Player, string, error) {
	where := " WHERE p.tenant_id = ?"
	args := []any{tenantID}
	if namePrefix != "" {
		pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(namePrefix)) + "%"
		where += ` AND EXISTS (SELECT 1 FROM player_names n WHERE n.tenant_id = p.tenant_id AND n.uuid = p.uuid
			AND lower(n.name) LIKE ? ESCAPE '\')`
		args = append(args, pattern)
	}
	cond, cargs, err := pageCond(page, "p.last_seen_at", "p.uuid")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT `+playerCols+` FROM players p`+where+cond+` ORDER BY p.last_seen_at DESC, p.uuid DESC LIMIT ?`,
		append(append(args, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.Player
	for rows.Next() {
		p, err := scanPlayer(tenantID, rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.LastSeenAt, l.UUID }), nil
}

func (s *Store) LookupVerdictTenant(ctx context.Context, verdictID string) (string, error) {
	var tenantID string
	err := s.queryRow(ctx, s.r, `SELECT tenant_id FROM verdicts WHERE id = ?`, verdictID).Scan(&tenantID)
	return tenantID, mapErr(err)
}

func (s *Store) DeleteVerdictLabels(ctx context.Context, tenantID, verdictID, label string) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM labels WHERE tenant_id = ? AND verdict_id = ? AND label = ?`, tenantID, verdictID, label)
}

func (s *Store) VerdictCategories(ctx context.Context, tenantID string, ids []string) (map[string]pb.Category, error) {
	out := make(map[string]pb.Category, len(ids))
	const chunk = 200
	for len(ids) > 0 {
		n := min(len(ids), chunk)
		args := []any{tenantID}
		for _, id := range ids[:n] {
			args = append(args, id)
		}
		ids = ids[n:]
		rows, err := s.query(ctx, s.r, `SELECT id, category FROM verdicts WHERE tenant_id = ? AND id IN (`+placeholders(n)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var c int64
			if err := rows.Scan(&id, &c); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = pb.Category(c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
