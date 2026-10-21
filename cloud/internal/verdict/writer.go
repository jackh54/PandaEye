package verdict

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// WriterConsumer is the durable consumer base name of the store writer on PE_VERDICTS.
const WriterConsumer = "writer"

const (
	writerQueue    = 256
	writerAttempts = 4
	writerTimeout  = 10 * time.Second
)

type writeJob struct {
	ctx  context.Context
	m    *stream.Msg
	done func(error)
}

// runWriter is the store writer (one leased instance, lease "writer"): it consumes PE_VERDICTS in
// order and persists VerdictRecords and AckRecords idempotently.
func (s *Service) runWriter(ctx context.Context, lease *stream.Lease) error {
	e, err := s.bus.ResetConsumer(ctx, stream.ConsumerSpec{Stream: stream.StreamVerdicts, Name: WriterConsumer}, "")
	if err != nil {
		return err
	}
	jobs := make(chan writeJob, writerQueue)
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-wctx.Done():
				return
			case j := <-jobs:
				j.done(s.write(j.ctx, j.m))
			}
		}
	}()
	_, err = s.bus.Consume(ctx, e, lease, func(hctx context.Context, m *stream.Msg, done func(error)) {
		select {
		case jobs <- writeJob{ctx: hctx, m: m, done: done}:
		case <-hctx.Done():
			done(context.Cause(hctx))
		}
	}, stream.ConsumeOptions{})
	cancel()
	<-stopped
	return err
}

// write persists one PE_VERDICTS message, retrying transient store errors. Malformed messages and
// permanent store errors are logged and skipped so they never block the stream.
func (s *Service) write(ctx context.Context, m *stream.Msg) error {
	var apply func(context.Context) error
	switch {
	case strings.HasPrefix(m.Subject, "pe.verdicts."):
		var rec pb.VerdictRecord
		if err := proto.Unmarshal(m.Data, &rec); err != nil {
			s.log.Warn("writer: undecodable verdict record skipped", "seq", m.Seq)
			return nil
		}
		apply = func(ctx context.Context) error { return s.storeRecord(ctx, &rec) }
	case strings.HasPrefix(m.Subject, "pe.acks."):
		var a pb.AckRecord
		if err := proto.Unmarshal(m.Data, &a); err != nil {
			s.log.Warn("writer: undecodable ack record skipped", "seq", m.Seq)
			return nil
		}
		apply = func(ctx context.Context) error { return s.storeAck(ctx, &a) }
	default:
		return nil
	}
	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, writerTimeout)
		err := apply(actx)
		cancel()
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrConflict):
			s.log.Warn("writer: record skipped", "seq", m.Seq, "subject", m.Subject, "err", err)
			return nil
		case attempt >= writerAttempts:
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

// storeRecord upserts a verdict (payload and clip links included). Records of erased players that
// predate the erasure are skipped.
func (s *Service) storeRecord(ctx context.Context, rec *pb.VerdictRecord) error {
	v := rec.Verdict
	if v == nil || !identity.ValidTenantID(rec.TenantId) || v.VerdictId == "" {
		s.log.Warn("writer: invalid verdict record skipped")
		return nil
	}
	pid, err := identity.UUIDFromBytes(v.Player)
	if err != nil {
		s.log.Warn("writer: verdict record without a player skipped", "verdict_id", v.VerdictId)
		return nil
	}
	created := time.UnixMicro(int64(rec.CreatedUnixUs)).UTC()
	if rec.CreatedUnixUs == 0 {
		created = time.UnixMilli(int64(v.CreatedUnixMs)).UTC()
	}
	if at, ok := s.erasedAt(rec.TenantId, pid); ok && !at.Before(created) {
		s.env.drop(dropErased)
		return nil
	}
	payload, err := proto.Marshal(v)
	if err != nil {
		return err
	}
	evidence := json.RawMessage(rec.EvidenceJson)
	if len(evidence) == 0 || !json.Valid(evidence) {
		evidence = nil
	}
	sv := &store.Verdict{
		ID: v.VerdictId, TenantID: rec.TenantId, ServerID: rec.ServerId, NetworkID: rec.NetworkId, PlayerUUID: pid.String(),
		PlayerName: v.PlayerName, SessionID: rec.SessionId, Action: v.Action, Category: v.Category, Score: v.Score,
		Confidence: v.Confidence, Reason: v.Reason, Checks: v.Checks, Evidence: evidence, ClipIDs: rec.ClipIds,
		Mitigations: v.Mitigations, Strength: v.Strength, BanDurationS: v.BanDurationS, Payload: payload, Shadow: v.Shadow,
		Status: store.StatusOpen, Supersedes: v.Supersedes, AppealCode: v.AppealCode, CreatedAt: created,
	}
	inserted, err := s.st.UpsertVerdict(ctx, sv)
	if err != nil {
		return err
	}
	if inserted {
		s.sink.RecordVerdict(rec)
	}
	return nil
}

// storeAck applies a plugin VerdictAck: acked/executed state, the bans row of an executed non-shadow
// BAN (with the executing server) and, for confirmed eligible bans, the reputation contribution.
func (s *Service) storeAck(ctx context.Context, a *pb.AckRecord) error {
	if !identity.ValidTenantID(a.TenantId) || a.VerdictId == "" {
		return nil
	}
	at := time.UnixMicro(int64(a.AckedUnixUs)).UTC()
	if a.AckedUnixUs == 0 {
		at = s.now()
	}
	res, err := s.st.ApplyAck(ctx, store.Ack{TenantID: a.TenantId, ServerID: a.ServerId, VerdictID: a.VerdictId,
		Executed: a.Executed, Note: a.Note, At: at})
	if errors.Is(err, store.ErrNotFound) {
		s.log.Debug("writer: ack for an unknown (or erased) verdict skipped", "verdict_id", a.VerdictId)
		return nil
	}
	if err != nil {
		return err
	}
	if v := res.Verdict; a.Executed && v != nil && ReputationEligible(v) {
		if _, err := ContributeReputation(ctx, s.st, a.TenantId, a.VerdictId, s.now()); err != nil {
			return err
		}
	}
	return nil
}
