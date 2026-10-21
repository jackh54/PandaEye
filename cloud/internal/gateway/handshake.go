package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// ProtocolVersion is the Hello.protocol_version this gateway serves.
const ProtocolVersion = 1

// handshake reads the Hello (first frame, codec 0x00, <= 16 KiB, within HelloTimeout) and admits
// the connection. false: the session was terminated.
func (s *session) handshake() bool {
	t := time.NewTimer(s.g.opts.HelloTimeout)
	defer t.Stop()
	var m inbound
	select {
	case msg, ok := <-s.msgs:
		if !ok {
			s.terminate("client", nil)
			return false
		}
		m = msg
	case <-t.C:
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "no hello within the handshake timeout", 0))
		return false
	case <-s.ctx.Done():
		return false
	}
	if m.typ != websocket.MessageBinary {
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "only binary messages are allowed", 0))
		return false
	}
	h, err := s.g.dec.DecodeHello(m.data)
	if err != nil {
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "the first frame must be a hello: "+err.Error(), 0))
		return false
	}
	return s.admit(h, m.at)
}

// admit runs the Hello checks of ARCHITECTURE 5.3 and sends the Welcome.
func (s *session) admit(h *pb.Hello, at time.Time) bool {
	g := s.g
	now := time.Now()
	if wait, blocked := g.adm.blocked(s.ip, now); blocked {
		s.fail(fatal(pb.Error_CODE_RATE_LIMITED, "too many failed licence checks from this address", millis(wait)))
		return false
	}
	switch {
	case h.ProtocolVersion != ProtocolVersion:
		s.fail(fatal(pb.Error_CODE_UNSUPPORTED_PROTOCOL, fmt.Sprintf("protocol version %d is not served (this gateway serves %d)", h.ProtocolVersion, ProtocolVersion), 0))
		return false
	case !identity.ValidServerID(h.ServerId):
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "server_id must be a canonical lowercase UUID", 0))
		return false
	case !identity.ValidServerID(h.InstanceId):
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "instance_id must be a canonical lowercase UUID", 0))
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	adm, err := g.resolver.Resolve(ctx, h.LicenseKey)
	switch {
	case errors.Is(err, license.ErrInvalid):
		g.adm.failure(s.ip, now)
		s.fail(fatal(pb.Error_CODE_UNAUTHENTICATED, "licence key invalid, expired or for another product", 0))
		return false
	case err != nil:
		s.log.Warn("licence verification unavailable", "err", err)
		s.fail(fatal(pb.Error_CODE_INTERNAL, "licence verification unavailable", 30_000))
		return false
	}
	t := adm.Tenant
	s.tenantID, s.serverID, s.hello = t.ID, h.ServerId, h
	s.tenantDef = tenantDefaults(t)
	s.online.Store(h.OnlinePlayers)
	if h.ClientTimeUnixMs > 0 {
		s.offsetUs, s.haveOffset = at.UnixMicro()-int64(h.ClientTimeUnixMs)*1000, true
	}
	s.log = s.log.With("tenant", t.ID, "server", h.ServerId)

	srv, rec, exists, err := s.lookupServer(ctx)
	if err != nil {
		s.log.Warn("server lookup failed", "err", err)
		s.fail(fatal(pb.Error_CODE_INTERNAL, "server lookup failed", 5_000))
		return false
	}
	if !exists || !identity.CheckSecret(srv.SecretHash, h.ServerSecret) {
		// Only reconnects presenting a valid server secret are exempt from the connection limits.
		if wait, ok := g.adm.allowConn(s.ip, now); !ok {
			s.fail(fatal(pb.Error_CODE_RATE_LIMITED, "too many new connections from this address", millis(wait)))
			return false
		}
		if wait, ok := g.adm.allowHello(t.ID, now); !ok {
			s.fail(fatal(pb.Error_CODE_RATE_LIMITED, "too many new connections for this licence", millis(wait)))
			return false
		}
		if exists {
			s.fail(fatal(pb.Error_CODE_DUPLICATE_SERVER, "this server_id is registered with another server secret; "+
				"if the plugin folder was copied from another server, delete server-id and server-secret", 30_000))
			return false
		}
	}

	rev, err := s.register(ctx)
	switch {
	case errors.Is(err, errDuplicateInstance):
		s.fail(fatal(pb.Error_CODE_DUPLICATE_SERVER, "another install with this server_id is connected (copied plugin folder?)", 30_000))
		return false
	case err != nil:
		s.log.Warn("registering the connection failed", "err", err)
		s.fail(fatal(pb.Error_CODE_INTERNAL, "connection registry unavailable", 5_000))
		return false
	}
	s.kvRev.Store(rev)
	if !s.withinServerLimit(ctx, adm.MaxServers) {
		return false
	}
	var secret identity.Secret
	if !exists {
		if secret, rec, err = s.createServer(ctx); err != nil {
			if errors.Is(err, store.ErrConflict) {
				s.fail(fatal(pb.Error_CODE_DUPLICATE_SERVER, "this server_id was registered concurrently by another install", 30_000))
			} else {
				s.log.Warn("registering the server failed", "err", err)
				s.fail(fatal(pb.Error_CODE_INTERNAL, "server registration failed", 5_000))
			}
			return false
		}
	} else {
		s.touchServer(srv, rec)
	}
	lastAcked := rec.LastAckedSeq
	if srv != nil {
		lastAcked = max(lastAcked, srv.LastBatchSeq)
	}
	s.seq.init(lastAcked)

	subj, err := stream.OutSubject(s.tenantID, s.serverID)
	if err == nil {
		s.sub, err = g.d.Bus.NC.Subscribe(subj, s.onOutbound)
	}
	if err == nil {
		err = s.sub.SetPendingLimits(g.opts.OutboundQueue, 64<<20)
	}
	if err == nil {
		err = g.d.Bus.NC.FlushTimeout(5 * time.Second) // the subscription is active before Welcome
	}
	if err != nil {
		s.log.Warn("subscribing to outbound messages failed", "err", err)
		s.fail(fatal(pb.Error_CODE_INTERNAL, "outbound subscription failed", 5_000))
		return false
	}
	if err := s.write(&pb.ServerFrame{Kind: &pb.ServerFrame_Welcome{Welcome: s.welcome(lastAcked, secret)}}); err != nil {
		s.terminate("write_failed", nil)
		return false
	}
	s.ready.Store(true)
	g.m.Auth.WithLabelValues("ok").Inc()
	s.log.Info("plugin connected", "platform", h.Platform.String(), "plugin_version", h.PluginVersion,
		"minecraft_version", h.MinecraftVersion, "shadow_mode", h.ShadowMode, "new_server", !exists,
		"tenant_created", adm.Created, "licence_fallback", adm.Fallback, "last_acked_seq", lastAcked)
	return true
}

func (s *session) welcome(lastAcked uint64, secret identity.Secret) *pb.Welcome {
	o := s.g.opts
	live, replay := o.LiveEventsPerS, o.ReplayEventsPerS
	if t := s.g.budgets.eventsPerS(s.settings().Plan); t > 0 {
		live, replay = min(live, t), min(replay, t)
	}
	return &pb.Welcome{
		ConnectionId: s.id, TenantId: s.tenantID, ServerTimeUnixMs: uint64(time.Now().UnixMilli()), Config: s.remoteConfig(),
		HeartbeatIntervalMs: uint32(o.HeartbeatInterval.Milliseconds()), MaxBatchEvents: uint32(o.MaxBatchEvents),
		LastAckedSeq: lastAcked, ServerSecret: secret.Reveal(), MaxEventsPerS: uint32(live), MaxBytesPerS: uint32(o.BytesPerS),
		ReplayMaxEventsPerS: uint32(replay),
	}
}

// lookupServer reads the server row and its PE_SERVERS record. When the database fails, the record
// alone is enough to check the secret.
func (s *session) lookupServer(ctx context.Context) (*store.Server, *stream.ServerRecord, bool, error) {
	bus := s.g.d.Bus
	srv, err := s.g.d.Store.GetServer(ctx, s.tenantID, s.serverID)
	rec, _, rerr := bus.GetServerRecord(ctx, s.tenantID, s.serverID)
	switch {
	case err == nil:
		if rerr != nil {
			rec = stream.ServerRecord{LastAckedSeq: srv.LastBatchSeq, SecretHash: srv.SecretHash, InstanceID: srv.InstanceID}
			if errors.Is(rerr, stream.ErrKeyNotFound) {
				if _, err := bus.CreateServerRecord(ctx, s.tenantID, s.serverID, rec); err != nil && !errors.Is(err, stream.ErrKeyExists) {
					s.log.Warn("restoring the server record failed", "err", err)
				}
			}
		}
		return srv, &rec, true, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, nil, false, nil
	case rerr == nil:
		s.log.Warn("server lookup failed, using the stream record", "err", err)
		return &store.Server{TenantID: s.tenantID, ID: s.serverID, SecretHash: rec.SecretHash, InstanceID: rec.InstanceID,
			LastBatchSeq: rec.LastAckedSeq}, &rec, true, nil
	default:
		return nil, nil, false, err
	}
}

// createServer registers a new server: row with the secret's hash, PE_SERVERS record, and the
// secret to send in the Welcome.
func (s *session) createServer(ctx context.Context) (identity.Secret, *stream.ServerRecord, error) {
	h := s.hello
	secret := identity.NewServerSecret()
	hash := identity.HashSecret(secret.Reveal())
	now := time.Now().UTC()
	err := s.g.d.Store.CreateServer(ctx, &store.Server{TenantID: s.tenantID, ID: s.serverID, Name: h.ServerName, NetworkID: h.NetworkId,
		Platform: h.Platform, PlatformVersion: h.PlatformVersion, PluginVersion: h.PluginVersion, MinecraftVersion: h.MinecraftVersion,
		SecretHash: hash, InstanceID: h.InstanceId, LastSeenAt: now, CreatedAt: now})
	if err != nil {
		return "", nil, err
	}
	rec := stream.ServerRecord{SecretHash: hash, InstanceID: h.InstanceId}
	bus := s.g.d.Bus
	if _, err := bus.CreateServerRecord(ctx, s.tenantID, s.serverID, rec); errors.Is(err, stream.ErrKeyExists) {
		// A record outlived its row (restored database): keep its seq, take the new secret.
		old, rev, gerr := bus.GetServerRecord(ctx, s.tenantID, s.serverID)
		if gerr == nil {
			rec.LastAckedSeq = old.LastAckedSeq
			_, err = bus.UpdateServerRecord(ctx, s.tenantID, s.serverID, rec, rev)
		}
		if err != nil && !errors.Is(err, stream.ErrKeyExists) {
			s.log.Warn("updating the server record failed", "err", err)
		}
	} else if err != nil {
		s.log.Warn("creating the server record failed", "err", err)
	}
	return secret, &rec, nil
}

// touchServer stores the Hello metadata of a known server in the background.
func (s *session) touchServer(srv *store.Server, rec *stream.ServerRecord) {
	h := s.hello
	row := *srv
	row.Name, row.NetworkID, row.Platform, row.PlatformVersion = h.ServerName, h.NetworkId, h.Platform, h.PlatformVersion
	row.PluginVersion, row.MinecraftVersion, row.InstanceID, row.LastSeenAt = h.PluginVersion, h.MinecraftVersion, h.InstanceId, time.Now().UTC()
	instanceChanged := rec.InstanceID != h.InstanceId
	s.runAux("server_seen", func(ctx context.Context) {
		if err := s.g.d.Store.UpdateServerSeen(ctx, &row); err != nil {
			s.log.Debug("storing server metadata failed", "err", err)
		}
		if !instanceChanged {
			return
		}
		for i := 0; i < 3; i++ {
			r, rev, err := s.g.d.Bus.GetServerRecord(ctx, s.tenantID, s.serverID)
			if err != nil {
				return
			}
			r.InstanceID = h.InstanceId
			if _, err := s.g.d.Bus.UpdateServerRecord(ctx, s.tenantID, s.serverID, r, rev); !isRevisionErr(err) {
				return
			}
		}
	})
}

// withinServerLimit enforces the licence's maxServers by recounting the tenant's registered
// connections; over the limit the connection's own entry is deleted. false: terminated.
func (s *session) withinServerLimit(ctx context.Context, maxServers int) bool {
	if maxServers <= 0 {
		return true
	}
	conns, err := s.g.d.Bus.ListConnections(ctx, s.tenantID)
	if err != nil {
		s.log.Warn("counting connected servers failed", "err", err)
		return true
	}
	if len(conns) <= maxServers {
		return true
	}
	if rev := s.kvRev.Swap(0); rev != 0 {
		if err := s.g.d.Bus.DeleteConnection(ctx, s.tenantID, s.serverID, rev); err != nil && !isRevisionErr(err) {
			s.log.Warn("releasing the connection entry failed", "err", err)
		}
	}
	s.fail(fatal(pb.Error_CODE_LICENSE_LIMIT, fmt.Sprintf("the licence allows %d connected servers", maxServers), 60_000))
	return false
}

func millis(d time.Duration) uint32 {
	ms := d.Milliseconds()
	switch {
	case ms < 1:
		return 1
	case ms > 1<<31:
		return 1 << 31
	}
	return uint32(ms)
}
