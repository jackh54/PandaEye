package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Queries of store.ModerationStore (moderation labels).

const moderationCols = `tenant_id, network_id, source, entry_id, kind, label, player_uuid, ban_start, removed_at, permanent,
	executor, received_at, applied_at, verdicts`

func scanModeration(sc interface{ Scan(...any) error }) (*store.ModerationLabel, error) {
	var m store.ModerationLabel
	var start, received int64
	var removed, applied sql.NullInt64
	if err := sc.Scan(&m.TenantID, &m.NetworkID, &m.Source, &m.EntryID, &m.Kind, &m.Label, &m.PlayerUUID, &start, &removed,
		&m.Permanent, &m.Executor, &received, &applied, &m.Verdicts); err != nil {
		return nil, mapErr(err)
	}
	m.BanStart, m.ReceivedAt = time.UnixMicro(start).UTC(), time.UnixMicro(received).UTC()
	m.RemovedAt, m.AppliedAt = fromUS(removed), fromUS(applied)
	return &m, nil
}

func (s *Store) RecordModerationLabel(ctx context.Context, m *store.ModerationLabel) (*store.ModerationLabel, bool, error) {
	m.ReceivedAt = orNow(m.ReceivedAt)
	res, err := s.exec(ctx, s.w, `INSERT INTO moderation_labels (`+moderationCols+`) VALUES (`+placeholders(14)+`)
		ON CONFLICT (tenant_id, network_id, source, entry_id, kind) DO NOTHING`,
		m.TenantID, m.NetworkID, m.Source, m.EntryID, m.Kind, m.Label, m.PlayerUUID, m.BanStart.UnixMicro(), us(m.RemovedAt),
		m.Permanent, m.Executor, m.ReceivedAt.UnixMicro(), us(m.AppliedAt), m.Verdicts)
	if err != nil {
		return nil, false, err
	}
	created := false
	if n, _ := res.RowsAffected(); n > 0 {
		created = true
	}
	stored, err := s.GetModerationLabel(ctx, m.TenantID, m.NetworkID, m.Source, m.EntryID, m.Kind)
	return stored, created, err
}

func (s *Store) GetModerationLabel(ctx context.Context, tenantID, networkID, source, entryID, kind string) (*store.ModerationLabel, error) {
	return scanModeration(s.queryRow(ctx, s.r, `SELECT `+moderationCols+` FROM moderation_labels
		WHERE tenant_id = ? AND network_id = ? AND source = ? AND entry_id = ? AND kind = ?`, tenantID, networkID, source, entryID, kind))
}

func (s *Store) MarkModerationLabelApplied(ctx context.Context, tenantID, networkID, source, entryID, kind string, verdicts int, at time.Time) error {
	return s.updateOne(ctx, `UPDATE moderation_labels SET applied_at = ?, verdicts = ?
		WHERE tenant_id = ? AND network_id = ? AND source = ? AND entry_id = ? AND kind = ?`, orNow(at).UnixMicro(), verdicts,
		tenantID, networkID, source, entryID, kind)
}

// notLifted keeps the cheat bans m that no wrongful unban of the same entry (of the same network and
// player) superseded. An unban of another player is not the ban's (the api applies it as unrelated).
const notLifted = `NOT EXISTS (SELECT 1 FROM moderation_labels l WHERE l.tenant_id = m.tenant_id AND l.network_id = m.network_id
	AND l.source = m.source AND l.entry_id = m.entry_id AND l.player_uuid = m.player_uuid AND l.label = ?)`

func (s *Store) ModerationAgreement(ctx context.Context, tenantID string, since, until time.Time, lookback time.Duration) (store.ModerationAgreement, error) {
	var out store.ModerationAgreement
	lb := lookback.Microseconds()
	alert, ban := int64(pb.Action_ACTION_ALERT), int64(pb.Action_ACTION_BAN)
	// Cheat bans of the period with the highest and the earliest of the player's verdicts before each.
	before := `FROM verdicts v WHERE v.tenant_id = m.tenant_id AND v.player_uuid = m.player_uuid AND v.action >= ? AND v.action <= ?
		AND v.created_at >= m.ban_start - ? AND v.created_at <= m.ban_start`
	rows, err := s.query(ctx, s.r, `SELECT m.player_uuid, m.ban_start, (SELECT MAX(v.action) `+before+`), (SELECT MIN(v.created_at) `+before+`)
		FROM moderation_labels m WHERE m.tenant_id = ? AND m.label = ? AND m.ban_start >= ? AND m.ban_start < ? AND `+notLifted+`
		ORDER BY m.ban_start, m.network_id, m.entry_id`, alert, ban, lb, alert, ban, lb, tenantID, store.LabelCheat, since.UnixMicro(),
		until.UnixMicro(), store.LabelLegit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b store.ModerationBan
		var start int64
		var top, first sql.NullInt64
		if err := rows.Scan(&b.PlayerUUID, &start, &top, &first); err != nil {
			return out, err
		}
		b.BanStart, b.TopAction, b.FirstVerdict = time.UnixMicro(start).UTC(), pb.Action(top.Int64), fromUS(first)
		out.Bans = append(out.Bans, b)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Players PandaEye would have kicked or banned in the period, and whether a cheat ban followed
	// within the lookback of one of those verdicts.
	err = s.queryRow(ctx, s.r, `SELECT COUNT(*), CAST(COALESCE(SUM(banned), 0) AS BIGINT) FROM (
			SELECT v.player_uuid, MAX(CASE WHEN EXISTS (SELECT 1 FROM moderation_labels m WHERE m.tenant_id = v.tenant_id
				AND m.player_uuid = v.player_uuid AND m.label = ? AND m.ban_start >= v.created_at AND m.ban_start <= v.created_at + ?
				AND `+notLifted+`) THEN 1 ELSE 0 END) AS banned
			FROM verdicts v WHERE v.tenant_id = ? AND v.action IN (?, ?) AND v.created_at >= ? AND v.created_at < ?
			GROUP BY v.player_uuid) f`,
		store.LabelCheat, lb, store.LabelLegit, tenantID, int64(pb.Action_ACTION_KICK), ban, since.UnixMicro(), until.UnixMicro()).
		Scan(&out.WouldAct, &out.WouldActBanned)
	if err != nil {
		return out, mapErr(err)
	}
	// Wrongful unbans of the period (by removal time; the time received when the plugin did not know it).
	err = s.queryRow(ctx, s.r, `SELECT COUNT(*), CAST(COALESCE(SUM(verdicts), 0) AS BIGINT) FROM moderation_labels
		WHERE tenant_id = ? AND label = ? AND COALESCE(removed_at, received_at) >= ? AND COALESCE(removed_at, received_at) < ?`,
		tenantID, store.LabelLegit, since.UnixMicro(), until.UnixMicro()).Scan(&out.Overturns, &out.VerdictsOverturned)
	return out, mapErr(err)
}

func (s *Store) PurgeModerationLabels(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	return s.deleteCount(ctx, `DELETE FROM moderation_labels WHERE tenant_id = ? AND received_at < ?`, tenantID, before.UnixMicro())
}
