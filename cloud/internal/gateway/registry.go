package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/nats-io/nats.go/jetstream"
)

// errDuplicateInstance: another install (different instance_id) with the same server_id is live.
var errDuplicateInstance = errors.New("gateway: another live install uses this server id")

func isRevisionErr(err error) bool {
	return errors.Is(err, stream.ErrRevisionMismatch) || errors.Is(err, jetstream.ErrKeyExists)
}

// connectionInfo is the session's PE_CONNECTIONS value.
func (s *session) connectionInfo() stream.ConnectionInfo {
	h := s.hello
	return stream.ConnectionInfo{ConnectionID: s.id, GatewayID: s.g.id, InstanceID: h.InstanceId, OnlinePlayers: s.online.Load(),
		NetworkID: h.NetworkId, Platform: h.Platform, ShadowMode: h.ShadowMode, ConnectedUnixMs: s.connectedMs,
		LastFrameUnixMs: s.lastRecv.Load() / 1e6}
}

// register creates the server's PE_CONNECTIONS entry. An existing entry of the same instance, or of
// another instance that has been silent for DuplicateSilence, is replaced and its connection closed;
// a live entry of another instance is a duplicate install.
func (s *session) register(ctx context.Context) (uint64, error) {
	bus := s.g.d.Bus
	s.connectedMs = time.Now().UnixMilli()
	for attempt := 0; attempt < 5; attempt++ {
		info := s.connectionInfo()
		rev, err := bus.CreateConnection(ctx, s.tenantID, s.serverID, info)
		if err == nil {
			return rev, nil
		}
		if !errors.Is(err, stream.ErrKeyExists) {
			return 0, err
		}
		old, err := bus.GetConnection(ctx, s.tenantID, s.serverID)
		if errors.Is(err, stream.ErrKeyNotFound) {
			continue // expired or released meanwhile
		}
		if err != nil {
			return 0, err
		}
		if old.Info.InstanceID != info.InstanceID && s.g.recentlyActive(old.Info) {
			return 0, errDuplicateInstance
		}
		rev, err = bus.UpdateConnection(ctx, s.tenantID, s.serverID, info, old.Revision)
		if isRevisionErr(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		s.log.Info("replacing the previous connection", "old_connection", old.Info.ConnectionID,
			"same_instance", old.Info.InstanceID == info.InstanceID)
		s.g.evict(s.tenantID, s.serverID, old.Info.ConnectionID)
		return rev, nil
	}
	return 0, errors.New("gateway: connection registration kept conflicting")
}

// recentlyActive reports whether a registered connection received a frame within DuplicateSilence
// (exactly for local connections, from the last renewal otherwise).
func (g *Service) recentlyActive(info stream.ConnectionInfo) bool {
	if ss := g.session(info.ConnectionID); ss != nil {
		return time.Since(time.Unix(0, ss.lastRecv.Load())) < g.opts.DuplicateSilence
	}
	return time.Since(time.UnixMilli(info.LastFrameUnixMs)) < g.opts.DuplicateSilence
}

// renew refreshes the PE_CONNECTIONS entry (TTL) at its revision. A mismatch means another
// connection replaced this one: close.
func (s *session) renew() {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	bus := s.g.d.Bus
	rev := s.kvRev.Load()
	if rev == 0 {
		return
	}
	next, err := bus.UpdateConnection(ctx, s.tenantID, s.serverID, s.connectionInfo(), rev)
	if err == nil {
		s.kvRev.CompareAndSwap(rev, next)
		return
	}
	if !isRevisionErr(err) {
		s.log.Warn("renewing the connection entry failed", "err", err)
		return
	}
	cur, gerr := bus.GetConnection(ctx, s.tenantID, s.serverID)
	switch {
	case gerr == nil && cur.Info.ConnectionID == s.id:
		s.kvRev.Store(cur.Revision) // an earlier renewal landed although its answer was lost
	case errors.Is(gerr, stream.ErrKeyNotFound):
		// Expired (the bus was unreachable for a whole TTL): register again unless someone else did.
		if next, cerr := bus.CreateConnection(ctx, s.tenantID, s.serverID, s.connectionInfo()); cerr == nil {
			s.kvRev.Store(next)
		} else if errors.Is(cerr, stream.ErrKeyExists) {
			s.kvRev.Store(0)
			s.terminate("replaced", nil)
		}
	case gerr == nil:
		s.log.Info("connection replaced by a newer one", "new_connection", cur.Info.ConnectionID)
		s.kvRev.Store(0)
		s.terminate("replaced", nil)
	default:
		s.log.Warn("reading the connection entry failed", "err", gerr)
	}
}
