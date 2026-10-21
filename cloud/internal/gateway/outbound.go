package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

// onOutbound receives pe.out.<tenant>.<server> (NATS callback): the SignedOutbound must verify with
// the current or the rotation key and address this connection's tenant and server.
func (s *session) onOutbound(m *nats.Msg) {
	out, err := stream.OutboundFor(m.Data, s.tenantID, s.serverID, s.g.d.Secrets.OutboundKeys()...)
	if err != nil {
		s.g.m.Drops.WithLabelValues("outbound_rejected").Inc()
		s.log.Warn("dropped an outbound message that does not verify for this connection", "err", err)
		return
	}
	switch k := out.Kind.(type) {
	case *pb.Outbound_CloseConnectionId:
		if k.CloseConnectionId == s.id {
			s.log.Info("closing: replaced by a newer connection on another gateway")
			s.terminate("replaced", nil)
		}
	case *pb.Outbound_Verdict:
		s.enqueue(outItem{verdict: k.Verdict, kind: "verdict"}, true)
	case *pb.Outbound_ProbeResult:
		s.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_ProbeResult{ProbeResult: k.ProbeResult}}, kind: "probe_result"}, true)
	case *pb.Outbound_Watch:
		s.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_Watch{Watch: k.Watch}}, kind: "watch"}, true)
	case *pb.Outbound_PlayerRisk:
		s.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_PlayerRisk{PlayerRisk: k.PlayerRisk}}, kind: "player_risk"}, true)
	case *pb.Outbound_ConfigUpdate:
		s.lastConfig.Store(k.ConfigUpdate)
		s.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_ConfigUpdate{ConfigUpdate: k.ConfigUpdate}}, kind: "config_update"}, true)
	}
}

// writeLoop drains the outbound queue.
func (s *session) writeLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case it := <-s.out:
			if !s.deliver(it) {
				return
			}
		}
	}
}

func (s *session) deliver(it outItem) bool {
	f := it.frame
	if v := it.verdict; v != nil {
		if s.deliveredRecently(v.VerdictId) {
			s.g.m.Drops.WithLabelValues("verdict_redelivery").Inc()
			return true
		}
		if !revalidate(v, s.settings()) {
			id := v.VerdictId
			s.runAux("expire", func(ctx context.Context) { s.expire(ctx, id) })
			return true
		}
		f = &pb.ServerFrame{Kind: &pb.ServerFrame_Verdict{Verdict: v}}
	}
	if pr := f.GetProbeResult(); pr != nil {
		now := time.Now().UnixMicro()
		pr.GatewaySendUnixUs = uint64(now)
		if pr.GatewayRecvUnixUs > 0 && uint64(now) >= pr.GatewayRecvUnixUs {
			s.g.m.ProbeRTT.WithLabelValues(metrics.StageCloud).Observe(float64(uint64(now)-pr.GatewayRecvUnixUs) / 1e6)
		}
		if pr.VerdictUnixUs > 0 && uint64(now) >= pr.VerdictUnixUs {
			s.g.m.ProbeRTT.WithLabelValues(metrics.StageVerdictToGateway).Observe(float64(uint64(now)-pr.VerdictUnixUs) / 1e6)
		}
	}
	if err := s.write(f); err != nil {
		s.terminate("write_failed", nil)
		return false
	}
	s.g.m.outbound.WithLabelValues(it.kind).Inc()
	if v := it.verdict; v != nil {
		id := v.VerdictId
		s.rememberDelivered(id, time.Now())
		s.runAux("delivered", func(ctx context.Context) {
			if err := s.g.d.Store.MarkDelivered(ctx, s.tenantID, []string{id}, time.Now()); err != nil {
				s.log.Debug("marking a verdict delivered failed", "verdict", id, "err", err)
			}
		})
	}
	return true
}

// revalidate applies the tenant's current flags to a verdict at delivery (ARCHITECTURE 5.3): a
// KICK or BAN no longer allowed goes out with shadow = true, any other verdict no longer allowed is
// not delivered (false: mark it expired); with enforcement off every verdict is shadow.
func revalidate(v *pb.Verdict, t stream.TenantSettings) bool {
	if !v.Shadow {
		switch v.Action {
		case pb.Action_ACTION_BAN:
			v.Shadow = !t.Autoban
		case pb.Action_ACTION_KICK:
			v.Shadow = !t.Autokick
		case pb.Action_ACTION_MITIGATE:
			if !t.Mitigations {
				return false
			}
		}
	}
	if !t.Enforcement {
		v.Shadow = true
	}
	return true
}

func (s *session) expire(ctx context.Context, id string) {
	err := s.g.d.Store.SetVerdictStatus(ctx, s.tenantID, id, store.StatusExpired, store.StatusOpen)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrConflict) {
		s.log.Warn("expiring a verdict failed", "verdict", id, "err", err)
	}
}

// pending queues the server's undelivered or unacknowledged verdicts: all of them right after the
// Welcome (fresh), those delivered more than ResendAfter ago on a sweep.
func (s *session) pending(ctx context.Context, fresh bool) {
	if s.ctx.Err() != nil {
		return
	}
	now := time.Now()
	q := store.PendingQuery{ServerID: s.serverID, Now: now, DeliveredBefore: now.Add(-s.g.opts.ResendAfter)}
	if fresh {
		q.DeliveredBefore = now
	}
	vs, err := s.g.d.Store.PendingVerdicts(ctx, s.tenantID, q)
	if err != nil {
		s.log.Warn("reading pending verdicts failed", "err", err)
		return
	}
	for i := range vs {
		v, err := verdictMessage(&vs[i])
		if err != nil {
			s.log.Warn("skipping an unreadable pending verdict", "verdict", vs[i].ID, "err", err)
			continue
		}
		s.enqueue(outItem{verdict: v, kind: "verdict"}, true)
	}
}

func (s *session) deliverPending(fresh bool) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	s.pending(ctx, fresh)
}

// verdictMessage returns the Verdict to send for a stored verdict: its serialised payload, or one
// rebuilt from the columns.
func verdictMessage(v *store.Verdict) (*pb.Verdict, error) {
	if len(v.Payload) > 0 {
		var pv pb.Verdict
		if err := proto.Unmarshal(v.Payload, &pv); err == nil && pv.VerdictId == v.ID {
			pv.Shadow = pv.Shadow || v.Shadow
			return &pv, nil
		}
	}
	player, err := identity.ParseCanonicalUUID(v.PlayerUUID)
	if err != nil {
		return nil, err
	}
	return &pb.Verdict{VerdictId: v.ID, Player: player.Bytes(), PlayerName: v.PlayerName, Action: v.Action, Mitigations: v.Mitigations,
		Strength: v.Strength, Reason: v.Reason, Category: v.Category, Score: v.Score, Confidence: v.Confidence, Checks: v.Checks,
		CreatedUnixMs: uint64(v.CreatedAt.UnixMilli()), Shadow: v.Shadow, BanDurationS: v.BanDurationS, Supersedes: v.Supersedes,
		AppealCode: v.AppealCode}, nil
}

// deliveredRecently implements the per-connection duplicate suppression of verdict ids.
func (s *session) deliveredRecently(id string) bool {
	s.deliveredMu.Lock()
	defer s.deliveredMu.Unlock()
	t, ok := s.delivered[id]
	return ok && time.Since(t) < s.g.opts.ResendAfter
}

func (s *session) rememberDelivered(id string, at time.Time) {
	s.deliveredMu.Lock()
	defer s.deliveredMu.Unlock()
	s.delivered[id] = at
}

func (s *session) pruneDelivered(now time.Time) {
	s.deliveredMu.Lock()
	defer s.deliveredMu.Unlock()
	for id, t := range s.delivered {
		if now.Sub(t) >= s.g.opts.ResendAfter {
			delete(s.delivered, id)
		}
	}
}

// handleVerdictAck relays a VerdictAck as AckRecord on pe.acks.<tenant> (msg id ack:<verdict id>).
func (s *session) handleVerdictAck(a *pb.VerdictAck, at time.Time) {
	if _, err := identity.ParseCanonicalUUID(a.VerdictId); err != nil {
		s.g.m.Drops.WithLabelValues("ack_invalid").Inc()
		return
	}
	subj, err := stream.AcksSubject(s.tenantID)
	if err != nil {
		return
	}
	data, err := proto.Marshal(&pb.AckRecord{TenantId: s.tenantID, ServerId: s.serverID, VerdictId: a.VerdictId,
		Executed: a.Executed, Note: a.Note, AckedUnixUs: uint64(at.UnixMicro())})
	if err != nil {
		return
	}
	msg := stream.Message{Subject: subj, Data: data, MsgID: stream.AckRecordMsgID(a.VerdictId)}
	s.runAux("ack", func(ctx context.Context) {
		if _, err := s.g.d.Bus.Publish(ctx, msg); err != nil {
			s.log.Warn("relaying a verdict ack failed", "verdict", a.VerdictId, "err", err)
		}
	})
}
