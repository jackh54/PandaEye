package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func (s *Store) InsertLabel(ctx context.Context, l *store.Label) error {
	if l.ID == "" {
		l.ID = identity.NewUUID().String()
	}
	l.CreatedAt = orNow(l.CreatedAt)
	_, err := s.exec(ctx, s.w, `INSERT INTO labels (id, tenant_id, player_uuid, verdict_id, label, source, window_start, window_end, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, l.ID, l.TenantID, l.PlayerUUID, l.VerdictID, l.Label, l.Source,
		l.WindowStart.UnixMicro(), l.WindowEnd.UnixMicro(), l.CreatedAt.UnixMicro())
	return err
}

func (s *Store) HasLegitCover(ctx context.Context, tenantID, player string, from, to time.Time) (bool, error) {
	var n int
	err := s.queryRow(ctx, s.r, `SELECT
		(SELECT COUNT(*) FROM labels WHERE tenant_id = ? AND player_uuid = ? AND label = ? AND window_start <= ? AND window_end >= ?) +
		(SELECT COUNT(*) FROM verdicts WHERE tenant_id = ? AND player_uuid = ? AND status = ? AND created_at >= ? AND created_at <= ?)`,
		tenantID, player, store.LabelLegit, to.UnixMicro(), from.UnixMicro(),
		tenantID, player, store.StatusOverturned, from.UnixMicro(), to.UnixMicro()).Scan(&n)
	return n > 0, mapErr(err)
}

func (s *Store) ExportLabels(ctx context.Context, tenantID string, since time.Time, page store.Page) ([]store.LabelExport, string, error) {
	cond, cargs, err := pageCondAsc(page, "l.created_at", "l.id")
	if err != nil {
		return nil, "", err
	}
	size := page.Size()
	rows, err := s.query(ctx, s.r, `SELECT l.id, l.player_uuid, l.verdict_id, l.label, l.source, l.window_start, l.window_end, l.created_at,
			v.action, v.score, v.checks, v.evidence
		FROM labels l LEFT JOIN verdicts v ON v.id = l.verdict_id AND v.tenant_id = l.tenant_id
		WHERE l.tenant_id = ? AND l.created_at >= ?
			AND NOT EXISTS (SELECT 1 FROM erasures e WHERE e.tenant_id = l.tenant_id AND e.player_uuid = l.player_uuid)`+cond+`
		ORDER BY l.created_at, l.id LIMIT ?`, append(append([]any{tenantID, since.UnixMicro()}, cargs...), size)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []store.LabelExport
	for rows.Next() {
		e := store.LabelExport{Label: store.Label{TenantID: tenantID}}
		var ws, we, created int64
		var action sql.NullInt64
		var score sql.NullFloat64
		var checks, evidence sql.NullString
		if err := rows.Scan(&e.ID, &e.PlayerUUID, &e.VerdictID, &e.Label.Label, &e.Source, &ws, &we, &created,
			&action, &score, &checks, &evidence); err != nil {
			return nil, "", err
		}
		e.WindowStart, e.WindowEnd, e.CreatedAt = time.UnixMicro(ws).UTC(), time.UnixMicro(we).UTC(), time.UnixMicro(created).UTC()
		if action.Valid {
			e.Action, e.Score, e.Evidence = pb.Action(action.Int64), score.Float64, json.RawMessage(evidence.String)
			if err := json.Unmarshal([]byte(checks.String), &e.Checks); err != nil {
				return nil, "", err
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	return out, nextCursor(len(out), size, func() (time.Time, string) { l := out[len(out)-1]; return l.CreatedAt, l.ID }), nil
}

func (s *Store) PurgeLabels(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM labels WHERE tenant_id = ? AND created_at < ?`, tenantID, before.UnixMicro())
}

func (s *Store) deleteCount(ctx context.Context, q string, args ...any) (int64, error) {
	res, err := s.exec(ctx, s.w, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
