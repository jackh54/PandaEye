package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// replayAge: a batch whose normalised send time is older than this counts as replayed.
const replayAge = 30 * time.Second

// handleBatch stores a batch in the event stream and acknowledges it only once every partition's
// StreamBatch has its PubAck. false ends the session.
func (s *session) handleBatch(b *pb.Batch, recv time.Time) bool {
	g := s.g
	recvUs := recv.UnixMicro()
	sentUs := int64(b.SentUnixMs) * 1000
	// Resent batches keep their original send time, so they do not feed the clock offset estimate.
	if b.SentUnixMs > 0 && !b.Replayed {
		if off := recvUs - sentUs; !s.haveOffset || off < s.offsetUs {
			s.offsetUs, s.haveOffset = off, true
		}
	}
	replayed := b.Replayed || (b.SentUnixMs > 0 && recvUs-(sentUs+s.offsetUs) > replayAge.Microseconds())

	n := len(b.Events)
	lim, budget := s.budget.live, "live"
	if replayed {
		lim, budget = s.budget.replay, "replay"
	}
	if !s.waitBudget(budget, lim, n) {
		return false
	}
	if tl := g.budgets.get(s.tenantID, s.settings().Plan, g.opts.MaxBatchEvents); tl != nil && !s.waitBudget("tenant", tl, n) {
		return false
	}

	msgs, err := s.partition(b, recvUs, replayed)
	if err != nil {
		return s.poison(b.Seq, metrics.BatchInvalid, "batch cannot be stored")
	}
	result := metrics.BatchStored
	if len(msgs) > 0 {
		ctx, cancel := context.WithTimeout(s.ctx, g.opts.PublishTimeout)
		acks, err := g.d.Bus.PublishAll(ctx, msgs)
		cancel()
		switch {
		case err == nil:
			dups := 0
			for _, a := range acks {
				if a.Duplicate {
					dups++
				}
			}
			if dups == len(acks) {
				result = metrics.BatchDuplicate
			}
		case errors.Is(err, stream.ErrPermanent):
			return s.poison(b.Seq, metrics.BatchTooLarge, "batch rejected by the event stream")
		case s.ctx.Err() != nil:
			return false // closing: the plugin resends the unacknowledged batch
		default:
			s.log.Error("event stream publish failed; closing so the plugin retries", "seq", b.Seq, "err", err)
			s.fail(fatal(pb.Error_CODE_INTERNAL, "event stream unavailable", 5_000))
			return false
		}
	}
	g.m.Batches.WithLabelValues(result).Inc()
	if result == metrics.BatchStored {
		g.m.Events.Add(float64(n))
	}
	if !s.ack(b.Seq) {
		return false
	}
	for _, e := range b.Events {
		if j := e.GetJoin(); j != nil && !j.Update && len(e.Player) == 16 {
			s.playerJoined(e.Player, j, recv)
		}
	}
	return true
}

// partition splits a batch into one StreamBatch per non-empty partition, keeping the event order:
// player events by the player's partition, probes round-robin by probe id, ServerStall copied into
// every partition, other server-level events by the server's partition.
func (s *session) partition(b *pb.Batch, recvUs int64, replayed bool) ([]stream.Message, error) {
	p := s.g.d.Bus.Partitioner
	parts := make([][]*pb.Event, p.Count())
	serverPart := -1
	for _, e := range b.Events {
		var i int
		switch k := e.Kind.(type) {
		case *pb.Event_Stall:
			for j := range parts {
				parts[j] = append(parts[j], e)
			}
			continue
		case *pb.Event_Probe:
			i = p.ForProbe(k.Probe.ProbeId)
		default:
			if len(e.Player) == len(identity.UUID{}) {
				i, _ = p.ForPlayer(e.Player)
			} else {
				if serverPart < 0 {
					var err error
					if serverPart, err = p.ForServer(s.serverID); err != nil {
						return nil, err
					}
				}
				i = serverPart
			}
		}
		parts[i] = append(parts[i], e)
	}
	origin := s.origin()
	var msgs []stream.Message
	for i, evs := range parts {
		if len(evs) == 0 {
			continue
		}
		data, err := proto.Marshal(&pb.StreamBatch{Origin: origin, BatchSeq: b.Seq, GatewayRecvUnixUs: uint64(recvUs),
			Replayed: replayed, Events: evs})
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, stream.Message{Subject: stream.EventsSubject(i), Data: data, MsgID: stream.EventsMsgID(s.tenantID, s.serverID, b.Seq, i)})
	}
	return msgs, nil
}

func (s *session) origin() *pb.Origin {
	h := s.hello
	return &pb.Origin{TenantId: s.tenantID, ServerId: s.serverID, ServerName: h.ServerName, NetworkId: h.NetworkId,
		ConnectionId: s.id, ShadowMode: h.ShadowMode, Platform: h.Platform, ClockOffsetUs: s.offsetUs, InstanceId: h.InstanceId}
}

// ack sends BatchAck{seq} and raises the server's last acked seq.
func (s *session) ack(seq uint64) bool {
	if err := s.write(&pb.ServerFrame{Kind: &pb.ServerFrame_BatchAck{BatchAck: &pb.BatchAck{Seq: seq}}}); err != nil {
		s.terminate("write_failed", nil)
		return false
	}
	s.seq.advance(seq)
	return true
}

// poison acknowledges and drops a batch that can never be stored (PROTOCOL 3.2):
// Error{CODE_TOO_LARGE, fatal=false}, then BatchAck.
func (s *session) poison(seq uint64, result, reason string) bool {
	s.g.m.Batches.WithLabelValues(result).Inc()
	s.log.Warn("poison batch acknowledged and dropped", "seq", seq, "result", result, "reason", reason)
	if err := s.write(errorFrame(&pb.Error{Code: pb.Error_CODE_TOO_LARGE, Message: reason})); err != nil {
		s.terminate("write_failed", nil)
		return false
	}
	return s.ack(seq)
}

// undecodable handles a frame Decode rejected: a batch whose seq can still be read is a poison batch;
// anything else is a fatal CODE_BAD_REQUEST.
func (s *session) undecodable(m inbound, err error) bool {
	if s.ctx.Err() != nil {
		return false
	}
	if errors.Is(err, codec.ErrTooLarge) || errors.Is(err, codec.ErrMalformed) {
		if seq, ok := peekBatchSeq(m.data); ok {
			result := metrics.BatchInvalid
			if errors.Is(err, codec.ErrTooLarge) {
				result = metrics.BatchTooLarge
			}
			return s.poison(seq, result, err.Error())
		}
	}
	s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "undecodable frame", 0))
	return false
}
