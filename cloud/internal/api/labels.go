package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// Moderation labels (ARCHITECTURE 5.8): the LiteBans bans for cheating and their wrongful unbans that
// the plugins classify and the worker forwards as LabelRecords on pe.labels.<tenant>. The label consumer
// (one leased instance, lease "labels") records each (tenant, network id, source, entry id, kind) once and
// decides the player's verdicts of the lookback before the ban, as staff would on the dashboard, except
// that a label never replaces a staff decision. Entry ids are those of one LiteBans database: the
// origin's network id keeps the entries of networks with their own database apart.

const (
	labelsConsumer     = "labels"
	labelsFilter       = "pe.labels.*"
	labelQueue         = 64
	labelAttempts      = 4
	labelTimeout       = 60 * time.Second
	maxLabelVerdicts   = 500 // verdicts one label decides at most
	maxAuditVerdictIDs = 50
)

// sourceLiteBans is the only moderation label source the cloud applies; liteBansPrincipal is the
// principal of its decisions (audit log, overturned bans).
const (
	sourceLiteBans    = "litebans"
	liteBansPrincipal = "litebans"
)

type labelJob struct {
	ctx  context.Context
	m    *stream.Msg
	done func(error)
}

// runLabels holds the labels lease and applies pe.labels.* in order (like the store writer, the
// handler hands each message to one goroutine so the consume loop never blocks).
func (s *Service) runLabels(ctx context.Context) error {
	return s.d.Bus.RunSingleton(ctx, stream.LeaseLabels, func(ctx context.Context, l *stream.Lease) error {
		e, err := s.d.Bus.ResetConsumer(ctx, stream.ConsumerSpec{Stream: stream.StreamVerdicts, Name: labelsConsumer, Filter: labelsFilter}, "")
		if err != nil {
			return err
		}
		jobs := make(chan labelJob, labelQueue)
		wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			for {
				select {
				case <-wctx.Done():
					return
				case j := <-jobs:
					j.done(s.applyLabelMsg(j.ctx, j.m))
				}
			}
		}()
		_, err = s.d.Bus.Consume(ctx, e, l, func(hctx context.Context, m *stream.Msg, done func(error)) {
			select {
			case jobs <- labelJob{ctx: hctx, m: m, done: done}:
			case <-hctx.Done():
				done(context.Cause(hctx))
			}
		}, stream.ConsumeOptions{})
		cancel()
		<-stopped
		return err
	})
}

// applyLabelMsg applies one LabelRecord, retrying transient store and bus errors. Malformed records,
// unknown sources and labels of unknown tenants are logged and skipped so they never block the stream.
func (s *Service) applyLabelMsg(ctx context.Context, m *stream.Msg) error {
	var rec pb.LabelRecord
	if err := proto.Unmarshal(m.Data, &rec); err != nil {
		s.log.Warn("labels: undecodable label record skipped", "seq", m.Seq)
		return nil
	}
	ml, err := moderationLabel(&rec, strings.TrimPrefix(m.Subject, "pe.labels."))
	if err != nil {
		s.log.Warn("labels: label record skipped", "seq", m.Seq, "err", err)
		return nil
	}
	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, labelTimeout)
		err := s.applyModerationLabel(actx, ml, s.now())
		cancel()
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound):
			s.log.Warn("labels: label skipped", "seq", m.Seq, "tenant_id", ml.TenantID, "network_id", ml.NetworkID, "entry_id", ml.EntryID,
				"err", err)
			return nil
		case attempt >= labelAttempts:
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// moderationLabel converts a LabelRecord of the subject's tenant into its stored row; the entry belongs to
// the network of the server that sent it (Origin.network_id, "" without one).
func moderationLabel(rec *pb.LabelRecord, subjectTenant string) (*store.ModerationLabel, error) {
	tenant := rec.GetOrigin().GetTenantId()
	if !identity.ValidTenantID(tenant) || tenant != subjectTenant {
		return nil, errors.New("tenant does not match the subject")
	}
	player, err := identity.UUIDFromBytes(rec.GetPlayer())
	if err != nil || player.IsZero() {
		return nil, errors.New("no player")
	}
	l := rec.GetLabel()
	if l.GetSource() != sourceLiteBans {
		return nil, fmt.Errorf("unknown label source %q", l.GetSource())
	}
	if l.GetEntryId() == 0 || l.GetBanStartUnixMs() == 0 {
		return nil, errors.New("no entry id or ban start")
	}
	m := &store.ModerationLabel{TenantID: tenant, NetworkID: rec.GetOrigin().GetNetworkId(), Source: l.GetSource(),
		EntryID: strconv.FormatUint(l.GetEntryId(), 10), PlayerUUID: player.String(), BanStart: time.UnixMilli(int64(l.GetBanStartUnixMs())).UTC(),
		Permanent: l.GetPermanent()}
	switch {
	case l.GetKind() == pb.ModerationLabel_KIND_BAN_ADDED && l.GetClassification() == pb.ModerationLabel_CLASSIFICATION_CHEAT:
		m.Kind, m.Label = store.ModerationBanAdded, store.LabelCheat
	case l.GetKind() == pb.ModerationLabel_KIND_BAN_REMOVED && l.GetClassification() == pb.ModerationLabel_CLASSIFICATION_LEGIT:
		m.Kind, m.Label = store.ModerationBanRemoved, store.LabelLegit
	default:
		return nil, fmt.Errorf("kind %v with classification %v", l.GetKind(), l.GetClassification())
	}
	if ms := l.GetRemovedUnixMs(); ms > 0 && m.Kind == store.ModerationBanRemoved {
		m.RemovedAt = time.UnixMilli(int64(ms)).UTC()
	}
	switch l.GetExecutor() {
	case pb.ModerationLabel_EXECUTOR_STAFF:
		m.Executor = store.ExecutorStaff
	case pb.ModerationLabel_EXECUTOR_CONSOLE:
		m.Executor = store.ExecutorConsole
	}
	return m, nil
}

// applyModerationLabel records m once per (tenant, network id, source, entry id, kind) and applies it: a
// cheat ban confirms the player's verdicts created in [ban start - lookback, ban start] (or, without one,
// labels the window before the ban cheat), a wrongful unban overturns them (or labels that window legit)
// and supersedes the entry's cheat labels. A cheat ban whose wrongful unban (of the same network) was
// recorded first is not applied, nor is a ban of an erased player that started before the erasure.
// Every step is idempotent: a retry after a failure completes it.
func (s *Service) applyModerationLabel(ctx context.Context, m *store.ModerationLabel, now time.Time) error {
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = now
	}
	// Every plugin enable resends the bans of the last backfill-days, also after an erasure (whose
	// tombstone only drops data received before it): the erased player's earlier bans stay erased
	// (5.13), a ban issued after the erasure is new data.
	switch er, err := s.d.Store.GetErasure(ctx, m.TenantID, m.PlayerUUID); {
	case err == nil && !m.BanStart.After(er.RequestedAt):
		s.log.Debug("moderation label of an erased player skipped", "tenant_id", m.TenantID, "network_id", m.NetworkID, "entry_id", m.EntryID)
		return nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return err
	}
	stored, _, err := s.d.Store.RecordModerationLabel(ctx, m)
	if err != nil {
		return err
	}
	if !stored.AppliedAt.IsZero() {
		return nil // the same entry from another server of the network, or a backfill on a plugin restart
	}
	var decided []string
	unflagged := false
	switch stored.Kind {
	case store.ModerationBanAdded:
		_, err := s.d.Store.GetModerationLabel(ctx, stored.TenantID, stored.NetworkID, stored.Source, stored.EntryID, store.ModerationBanRemoved)
		switch {
		case err == nil:
			// Lifted as wrong before we saw the ban (out of order): nothing to confirm.
		case !errors.Is(err, store.ErrNotFound):
			return err
		default:
			if decided, unflagged, err = s.applyCheatBan(ctx, stored, now); err != nil {
				return err
			}
		}
	case store.ModerationBanRemoved:
		if decided, unflagged, err = s.applyWrongfulUnban(ctx, stored, now); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: moderation label kind %q", store.ErrInvalid, stored.Kind)
	}
	err = s.d.Store.MarkModerationLabelApplied(ctx, stored.TenantID, stored.NetworkID, stored.Source, stored.EntryID, stored.Kind, len(decided), now)
	if err != nil {
		return err
	}
	ids := decided
	if len(ids) > maxAuditVerdictIDs {
		ids = ids[:maxAuditVerdictIDs]
	}
	s.audit(ctx, stored.TenantID, liteBansPrincipal, "litebans."+stored.Label, stored.Source+":"+stored.EntryID,
		map[string]any{"network_id": stored.NetworkID, "player_uuid": stored.PlayerUUID, "verdicts": len(decided), "verdict_ids": ids,
			"unflagged_label": unflagged, "executor": stored.Executor}, now)
	s.log.Debug("moderation label applied", "tenant_id", stored.TenantID, "label", stored.Label, "verdicts", len(decided), "unflagged", unflagged)
	return nil
}

// lookbackVerdicts returns the player's verdicts (not UNBAN/UNMITIGATE) created in
// [ban start - lookback, ban start], newest first, at most maxLabelVerdicts.
func (s *Service) lookbackVerdicts(ctx context.Context, m *store.ModerationLabel) ([]store.Verdict, error) {
	f := store.VerdictFilter{PlayerUUID: m.PlayerUUID, Since: m.BanStart.Add(-s.d.Config.Labels.Lookback), Until: m.BanStart.Add(time.Microsecond)}
	var out []store.Verdict
	page := store.Page{Limit: store.MaxPageLimit}
	for {
		vs, next, err := s.d.Store.ListVerdicts(ctx, m.TenantID, f, page)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if !corrective(v.Action) && len(out) < maxLabelVerdicts {
				out = append(out, v)
			}
		}
		if next == "" || len(out) >= maxLabelVerdicts {
			return out, nil
		}
		page.Cursor = next
	}
}

// applyCheatBan confirms the verdicts of the lookback (those staff decided or that are overturned stay
// as they are); without any, the player gets an unflagged cheat label over the window before the ban.
func (s *Service) applyCheatBan(ctx context.Context, m *store.ModerationLabel, now time.Time) ([]string, bool, error) {
	vs, err := s.lookbackVerdicts(ctx, m)
	if err != nil {
		return nil, false, err
	}
	if len(vs) == 0 {
		return nil, true, s.insertUnflaggedLabel(ctx, m, now)
	}
	var decided []string
	for i := range vs {
		_, err := s.confirm(ctx, &vs[i], liteBansDecision(m), now)
		switch {
		case errors.Is(err, store.ErrConflict):
		case err != nil:
			return nil, false, err
		default:
			decided = append(decided, vs[i].ID)
		}
	}
	return decided, false, nil
}

// applyWrongfulUnban supersedes the entry's cheat labels and overturns the verdicts of the lookback
// that staff did not decide; without any verdict, the player gets an unflagged legit label over the
// window before the ban.
func (s *Service) applyWrongfulUnban(ctx context.Context, m *store.ModerationLabel, now time.Time) ([]string, bool, error) {
	// The cheat labels the ban wrote: the unflagged window, and the verdicts' labels (including those
	// of verdicts purged since).
	for _, del := range []struct {
		source   string
		from, to time.Time
	}{
		{store.SourceLiteBansUnflagged, m.BanStart, m.BanStart},
		{store.SourceLiteBans, m.BanStart.Add(-s.d.Config.Labels.Lookback), m.BanStart},
	} {
		if _, err := s.d.Store.DeletePlayerLabels(ctx, m.TenantID, m.PlayerUUID, store.LabelCheat, del.source, del.from, del.to); err != nil {
			return nil, false, err
		}
	}
	vs, err := s.lookbackVerdicts(ctx, m)
	if err != nil {
		return nil, false, err
	}
	if len(vs) == 0 {
		return nil, true, s.insertUnflaggedLabel(ctx, m, now)
	}
	var decided []string
	for i := range vs {
		if vs[i].DecidedBy == store.DecidedByStaff {
			continue
		}
		_, err := s.overturn(ctx, &vs[i], liteBansDecision(m), now)
		switch {
		case errors.Is(err, store.ErrConflict):
		case err != nil:
			return nil, false, err
		default:
			decided = append(decided, vs[i].ID)
		}
	}
	return decided, false, nil
}

// insertUnflaggedLabel labels the player over [ban start - window, ban start]: cheat for a ban, legit
// for a wrongful unban (one label per network's entry and value).
func (s *Service) insertUnflaggedLabel(ctx context.Context, m *store.ModerationLabel, now time.Time) error {
	l := &store.Label{
		ID:       uuid.NewSHA1(labelNamespace, []byte(m.TenantID+"|"+m.NetworkID+"|"+m.Source+":"+m.EntryID+"|"+m.Label)).String(),
		TenantID: m.TenantID, PlayerUUID: m.PlayerUUID, Label: m.Label, Source: store.SourceLiteBansUnflagged,
		WindowStart: m.BanStart.Add(-s.d.Config.Labels.Window), WindowEnd: m.BanStart, CreatedAt: now,
	}
	err := s.d.Store.InsertLabel(ctx, l)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}
