package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

const verdictCols = `id, tenant_id, server_id, network_id, player_uuid, player_name, session_id, action, category, score,
	confidence, reason, checks, evidence, clip_ids, mitigations, strength, ban_duration_s, payload, shadow, status,
	supersedes, appeal_code, created_at, delivered_at, acked_at, executed, ack_note`

func scanVerdict(sc interface{ Scan(...any) error }) (*store.Verdict, error) {
	var v store.Verdict
	var action, category, banDuration int64
	var checks, evidence, clips, mitigations string
	var created, delivered, acked sql.NullInt64
	if err := sc.Scan(&v.ID, &v.TenantID, &v.ServerID, &v.NetworkID, &v.PlayerUUID, &v.PlayerName, &v.SessionID, &action,
		&category, &v.Score, &v.Confidence, &v.Reason, &checks, &evidence, &clips, &mitigations, &v.Strength, &banDuration,
		&v.Payload, &v.Shadow, &v.Status, &v.Supersedes, &v.AppealCode, &created, &delivered, &acked, &v.Executed,
		&v.AckNote); err != nil {
		return nil, mapErr(err)
	}
	v.Action, v.Category, v.BanDurationS = pb.Action(action), pb.Category(category), uint32(banDuration)
	v.Evidence = json.RawMessage(evidence)
	if err := unmarshalAll([]string{checks, clips}, &v.Checks, &v.ClipIDs); err != nil {
		return nil, err
	}
	var ms []int32
	if err := json.Unmarshal([]byte(mitigations), &ms); err != nil {
		return nil, err
	}
	for _, m := range ms {
		v.Mitigations = append(v.Mitigations, pb.Mitigation(m))
	}
	v.CreatedAt, v.DeliveredAt, v.AckedAt = fromUS(created), fromUS(delivered), fromUS(acked)
	return &v, nil
}

func unmarshalAll(src []string, dst ...any) error {
	for i, s := range src {
		if err := json.Unmarshal([]byte(s), dst[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) scanVerdicts(rows *sql.Rows) ([]store.Verdict, error) {
	defer rows.Close()
	var out []store.Verdict
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (s *Store) UpsertVerdict(ctx context.Context, v *store.Verdict) (bool, error) {
	v.CreatedAt = orNow(v.CreatedAt)
	if v.Status == "" {
		v.Status = store.StatusOpen
	}
	evidence := string(v.Evidence)
	if len(v.Evidence) == 0 {
		evidence = "{}"
	}
	if !json.Valid([]byte(evidence)) {
		return false, fmt.Errorf("%w: evidence is not JSON", store.ErrInvalid)
	}
	ms := make([]int32, len(v.Mitigations))
	for i, m := range v.Mitigations {
		ms[i] = int32(m)
	}
	created := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, `INSERT INTO verdicts (`+verdictCols+`) VALUES (`+placeholders(28)+`) ON CONFLICT (id) DO NOTHING`,
			v.ID, v.TenantID, v.ServerID, v.NetworkID, v.PlayerUUID, v.PlayerName, v.SessionID, int64(v.Action),
			int64(v.Category), v.Score, v.Confidence, v.Reason, jsonText(v.Checks, "[]"), evidence, jsonText(v.ClipIDs, "[]"),
			jsonText(ms, "[]"), v.Strength, int64(v.BanDurationS), v.Payload, v.Shadow, v.Status, v.Supersedes, v.AppealCode,
			v.CreatedAt.UnixMicro(), us(v.DeliveredAt), us(v.AckedAt), v.Executed, v.AckNote)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		created = true
		for _, c := range v.ClipIDs {
			if _, err := s.exec(ctx, tx, `INSERT INTO verdict_clips (tenant_id, verdict_id, clip_id) VALUES (?, ?, ?)
				ON CONFLICT DO NOTHING`, v.TenantID, v.ID, c); err != nil {
				return err
			}
		}
		return nil
	})
	return created, err
}

func (s *Store) GetVerdict(ctx context.Context, tenantID, id string) (*store.Verdict, error) {
	return scanVerdict(s.queryRow(ctx, s.r, `SELECT `+verdictCols+` FROM verdicts WHERE id = ? AND tenant_id = ?`, id, tenantID))
}

func (s *Store) ListVerdicts(ctx context.Context, tenantID string, f store.VerdictFilter, page store.Page) ([]store.Verdict, string, error) {
	where := " WHERE tenant_id = ?"
	args := []any{tenantID}
	if f.Action != pb.Action_ACTION_UNSPECIFIED {
		where += " AND action = ?"
		args = append(args, int64(f.Action))
	}
	if f.Status != "" {
		where += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.PlayerUUID != "" {
		where += " AND player_uuid = ?"
		args = append(args, f.PlayerUUID)
	}
	if f.ServerID != "" {
		where += " AND server_id = ?"
		args = append(args, f.ServerID)
	}
	if !f.Since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, f.Since.UnixMicro())
	}
	if !f.Until.IsZero() {
		where += " AND created_at < ?"
		args = append(args, f.Until.UnixMicro())
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

func (s *Store) ListVerdictsBySession(ctx context.Context, tenantID, sessionID string) ([]store.Verdict, error) {
	rows, err := s.query(ctx, s.r, `SELECT `+verdictCols+` FROM verdicts WHERE tenant_id = ? AND session_id = ? ORDER BY created_at, id`,
		tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	return s.scanVerdicts(rows)
}

func (s *Store) PendingVerdicts(ctx context.Context, tenantID string, q store.PendingQuery) ([]store.Verdict, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	n := orNow(q.Now)
	rows, err := s.query(ctx, s.r, `SELECT `+verdictCols+` FROM verdicts
		WHERE tenant_id = ? AND server_id = ? AND status = ? AND acked_at IS NULL
			AND (delivered_at IS NULL OR delivered_at < ?)
			AND (action IN (?, ?)
				OR (action = ? AND created_at > ?)
				OR (action NOT IN (?, ?, ?) AND created_at > ?))
		ORDER BY created_at, id LIMIT ?`,
		tenantID, q.ServerID, store.StatusOpen, us(q.DeliveredBefore),
		int64(pb.Action_ACTION_UNBAN), int64(pb.Action_ACTION_UNMITIGATE),
		int64(pb.Action_ACTION_KICK), n.Add(-store.PendingKickMaxAge).UnixMicro(),
		int64(pb.Action_ACTION_KICK), int64(pb.Action_ACTION_UNBAN), int64(pb.Action_ACTION_UNMITIGATE), n.Add(-store.PendingMaxAge).UnixMicro(),
		limit)
	if err != nil {
		return nil, err
	}
	return s.scanVerdicts(rows)
}

func (s *Store) MarkDelivered(ctx context.Context, tenantID string, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{orNow(at).UnixMicro(), tenantID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.exec(ctx, s.w, `UPDATE verdicts SET delivered_at = ? WHERE tenant_id = ? AND id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

func (s *Store) ApplyAck(ctx context.Context, a store.Ack) (store.AckResult, error) {
	var res store.AckResult
	at := orNow(a.At).UnixMicro()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		r, err := s.exec(ctx, tx, `UPDATE verdicts SET acked_at = COALESCE(acked_at, ?), executed = (executed OR ?),
			ack_note = CASE WHEN ? <> '' THEN ? ELSE ack_note END WHERE id = ? AND tenant_id = ?`,
			at, a.Executed, a.Note, a.Note, a.VerdictID, a.TenantID)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		v, err := scanVerdict(s.queryRow(ctx, tx, `SELECT `+verdictCols+` FROM verdicts WHERE id = ? AND tenant_id = ?`, a.VerdictID, a.TenantID))
		if err != nil {
			return err
		}
		res.Verdict = v
		if !a.Executed {
			return nil
		}
		switch {
		case v.Action == pb.Action_ACTION_BAN && !v.Shadow:
			r, err := s.exec(ctx, tx, `INSERT INTO bans (verdict_id, tenant_id, server_id, player_uuid, active, reason, created_at, unban_state)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (verdict_id) DO NOTHING`,
				v.ID, v.TenantID, a.ServerID, v.PlayerUUID, true, v.Reason, at, store.UnbanNone)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			res.BanCreated = n > 0
		case v.Action == pb.Action_ACTION_UNBAN && v.Supersedes != "":
			r, err := s.exec(ctx, tx, `UPDATE bans SET unban_state = ? WHERE tenant_id = ? AND verdict_id = ? AND unban_verdict_id = ?`,
				store.UnbanDone, v.TenantID, v.Supersedes, v.ID)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			res.UnbanDone = n > 0
		}
		return nil
	})
	return res, err
}

func (s *Store) SetVerdictStatus(ctx context.Context, tenantID, id, to string, from ...string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var cur string
		if err := s.queryRow(ctx, tx, `SELECT status FROM verdicts WHERE id = ? AND tenant_id = ?`+s.forUpdate(), id, tenantID).Scan(&cur); err != nil {
			return mapErr(err)
		}
		if len(from) > 0 && !contains(from, cur) {
			return store.ErrConflict
		}
		_, err := s.exec(ctx, tx, `UPDATE verdicts SET status = ? WHERE id = ? AND tenant_id = ?`, to, id, tenantID)
		return err
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Store) VerdictCounts(ctx context.Context, tenantID string, since time.Time) ([]store.VerdictCount, error) {
	rows, err := s.query(ctx, s.r, `SELECT action, category, COUNT(*) FROM verdicts WHERE tenant_id = ? AND created_at >= ?
		GROUP BY action, category ORDER BY action, category`, tenantID, since.UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.VerdictCount
	for rows.Next() {
		var a, c, n int64
		if err := rows.Scan(&a, &c, &n); err != nil {
			return nil, err
		}
		out = append(out, store.VerdictCount{Action: pb.Action(a), Category: pb.Category(c), Count: n})
	}
	return out, rows.Err()
}

func (s *Store) PurgeVerdicts(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	res, err := s.exec(ctx, s.w, `DELETE FROM verdicts WHERE tenant_id = ? AND created_at < ? AND NOT EXISTS (
		SELECT 1 FROM bans b WHERE b.verdict_id = verdicts.id AND b.tenant_id = verdicts.tenant_id AND b.active = ?)`,
		tenantID, before.UnixMicro(), true)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
