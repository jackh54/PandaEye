package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go/jetstream"
)

// KV errors, re-exported for callers of the helpers below.
var (
	ErrKeyNotFound      = jetstream.ErrKeyNotFound
	ErrKeyExists        = jetstream.ErrKeyExists
	ErrRevisionMismatch = jetstream.ErrKeyRevisionMismatch
)

// CheckSettings is the per-tenant configuration of one cloud check (tenant_checks).
type CheckSettings struct {
	Enabled   bool               `json:"enabled"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// TenantSettings is the value of KV PE_TENANTS[<tenant>]: what the api writes after every settings
// change and the gateway, workers and verdict services watch.
type TenantSettings struct {
	TenantID          string                   `json:"tenant_id"`
	Leniency          int                      `json:"leniency"`
	Autoban           bool                     `json:"autoban"`
	Autokick          bool                     `json:"autokick"`
	Mitigations       bool                     `json:"mitigations"`
	Enforcement       bool                     `json:"enforcement"`
	ReputationOptIn   bool                     `json:"reputation_opt_in"`
	DiscordWebhookEnc []byte                   `json:"discord_webhook_enc,omitempty"` // identity.Seal output
	DiscordMinAction  pb.Action                `json:"discord_min_action"`
	Plan              string                   `json:"plan"`
	MaxServers        int                      `json:"max_servers"`
	OverLimitSince    int64                    `json:"over_limit_since_unix_ms,omitempty"` // free tier over limit
	Checks            map[string]CheckSettings `json:"checks,omitempty"`
	UpdatedUnixMs     int64                    `json:"updated_unix_ms"`
}

// RemoteConfig returns the plugin-facing settings.
func (t TenantSettings) RemoteConfig() *pb.RemoteConfig {
	return &pb.RemoteConfig{Leniency: uint32(t.Leniency), Autoban: t.Autoban, Autokick: t.Autokick,
		Mitigations: t.Mitigations, ReputationOnJoin: t.ReputationOptIn, Enforcement: t.Enforcement}
}

// CheckEnabled reports whether a check is enabled for the tenant; checks without a tenant entry use
// def (the catalog default).
func (t TenantSettings) CheckEnabled(checkID string, def bool) bool {
	if c, ok := t.Checks[checkID]; ok {
		return c.Enabled
	}
	return def
}

// PutTenant writes the settings of a tenant.
func (b *Bus) PutTenant(ctx context.Context, s TenantSettings) (uint64, error) {
	if !identity.ValidTenantID(s.TenantID) {
		return 0, ErrBadToken
	}
	data, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	return b.Tenants.Put(ctx, s.TenantID, data)
}

// GetTenant reads the settings of a tenant (ErrKeyNotFound when none were written).
func (b *Bus) GetTenant(ctx context.Context, tenantID string) (TenantSettings, uint64, error) {
	var s TenantSettings
	if !identity.ValidTenantID(tenantID) {
		return s, 0, ErrBadToken
	}
	e, err := b.Tenants.Get(ctx, tenantID)
	if err != nil {
		return s, 0, err
	}
	return s, e.Revision(), json.Unmarshal(e.Value(), &s)
}

// ConnectionInfo is the value of KV PE_CONNECTIONS[<tenant>.<server>] (TTL 30 s, renewed by the
// gateway every 10 s with revision checks).
type ConnectionInfo struct {
	ConnectionID    string      `json:"connection_id"`
	GatewayID       string      `json:"gateway_id"`
	InstanceID      string      `json:"instance_id"`
	OnlinePlayers   uint32      `json:"online_players"`
	NetworkID       string      `json:"network_id,omitempty"`
	Platform        pb.Platform `json:"platform"`
	ShadowMode      bool        `json:"shadow_mode"`
	ConnectedUnixMs int64       `json:"connected_unix_ms"`
	LastFrameUnixMs int64       `json:"last_frame_unix_ms"` // duplicate-install rule: silent for 15 s
}

// Connection is one entry of PE_CONNECTIONS.
type Connection struct {
	TenantID string
	ServerID string
	Info     ConnectionInfo
	Revision uint64
}

// CreateConnection registers a connection; ErrKeyExists when the server is connected already.
func (b *Bus) CreateConnection(ctx context.Context, tenantID, serverID string, info ConnectionInfo) (uint64, error) {
	return createJSON(ctx, b.Connections, tenantID, serverID, info)
}

// UpdateConnection renews a connection at its last revision; ErrRevisionMismatch means it was
// replaced (close the connection).
func (b *Bus) UpdateConnection(ctx context.Context, tenantID, serverID string, info ConnectionInfo, rev uint64) (uint64, error) {
	return updateJSON(ctx, b.Connections, tenantID, serverID, info, rev)
}

// GetConnection reads a connection entry.
func (b *Bus) GetConnection(ctx context.Context, tenantID, serverID string) (Connection, error) {
	c := Connection{TenantID: tenantID, ServerID: serverID}
	rev, err := getJSON(ctx, b.Connections, tenantID, serverID, &c.Info)
	c.Revision = rev
	return c, err
}

// DeleteConnection removes a connection entry if it still has revision rev.
func (b *Bus) DeleteConnection(ctx context.Context, tenantID, serverID string, rev uint64) error {
	k, err := ServerKey(tenantID, serverID)
	if err != nil {
		return err
	}
	return b.Connections.Delete(ctx, k, jetstream.LastRevision(rev))
}

// ListConnections returns the live connections of one tenant, or of every tenant when tenantID is
// empty (metering, /status).
func (b *Bus) ListConnections(ctx context.Context, tenantID string) ([]Connection, error) {
	filter := ">"
	if tenantID != "" {
		if !identity.ValidTenantID(tenantID) {
			return nil, ErrBadToken
		}
		filter = tenantID + ".*"
	}
	w, err := b.Connections.Watch(ctx, filter, jetstream.IgnoreDeletes())
	if err != nil {
		return nil, err
	}
	defer w.Stop()
	var out []Connection
	for e := range w.Updates() {
		if e == nil {
			return out, nil
		}
		t, s, err := ParseServerKey(e.Key())
		if err != nil {
			continue
		}
		c := Connection{TenantID: t, ServerID: s, Revision: e.Revision()}
		if json.Unmarshal(e.Value(), &c.Info) == nil {
			out = append(out, c)
		}
	}
	return out, ctx.Err()
}

// ServerRecord is the value of KV PE_SERVERS[<tenant>.<server>].
type ServerRecord struct {
	LastAckedSeq uint64 `json:"last_acked_seq"`
	SecretHash   []byte `json:"secret_hash"`
	InstanceID   string `json:"instance_id"`
}

// CreateServerRecord stores a new server's record; ErrKeyExists when it exists.
func (b *Bus) CreateServerRecord(ctx context.Context, tenantID, serverID string, r ServerRecord) (uint64, error) {
	return createJSON(ctx, b.Servers, tenantID, serverID, r)
}

// GetServerRecord reads a server's record (ErrKeyNotFound when none).
func (b *Bus) GetServerRecord(ctx context.Context, tenantID, serverID string) (ServerRecord, uint64, error) {
	var r ServerRecord
	rev, err := getJSON(ctx, b.Servers, tenantID, serverID, &r)
	return r, rev, err
}

// UpdateServerRecord writes a server's record at revision rev.
func (b *Bus) UpdateServerRecord(ctx context.Context, tenantID, serverID string, r ServerRecord, rev uint64) (uint64, error) {
	return updateJSON(ctx, b.Servers, tenantID, serverID, r, rev)
}

// AdvanceServerSeq raises LastAckedSeq to seq (never lowers it), retrying on concurrent updates, and
// returns the stored record.
func (b *Bus) AdvanceServerSeq(ctx context.Context, tenantID, serverID string, seq uint64) (ServerRecord, error) {
	for {
		r, rev, err := b.GetServerRecord(ctx, tenantID, serverID)
		if err != nil {
			return r, err
		}
		if r.LastAckedSeq >= seq {
			return r, nil
		}
		r.LastAckedSeq = seq
		if _, err := b.UpdateServerRecord(ctx, tenantID, serverID, r, rev); err == nil {
			return r, nil
		} else if !errors.Is(err, ErrRevisionMismatch) {
			return r, err
		}
	}
}

// PutErasure publishes the erasure tombstone of a player (TTL ErasureTTL).
func (b *Bus) PutErasure(ctx context.Context, tenantID string, player identity.UUID, at time.Time) error {
	k, err := ErasureKey(tenantID, player)
	if err != nil {
		return err
	}
	_, err = b.Erasures.Put(ctx, k, []byte(fmt.Sprint(at.UnixMilli())))
	return err
}

func createJSON(ctx context.Context, kv jetstream.KeyValue, tenantID, serverID string, v any) (uint64, error) {
	k, err := ServerKey(tenantID, serverID)
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	return kv.Create(ctx, k, data)
}

func updateJSON(ctx context.Context, kv jetstream.KeyValue, tenantID, serverID string, v any, rev uint64) (uint64, error) {
	k, err := ServerKey(tenantID, serverID)
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	return kv.Update(ctx, k, data, rev)
}

func getJSON(ctx context.Context, kv jetstream.KeyValue, tenantID, serverID string, v any) (uint64, error) {
	k, err := ServerKey(tenantID, serverID)
	if err != nil {
		return 0, err
	}
	e, err := kv.Get(ctx, k)
	if err != nil {
		return 0, err
	}
	return e.Revision(), json.Unmarshal(e.Value(), v)
}

// Watch mirrors a whole KV bucket in memory: Ready is closed once the initial values are loaded, Get
// reads the current value, OnChange registers callbacks (run on the watch goroutine, in order).
type Watch[T any] struct {
	mu        sync.RWMutex
	values    map[string]T
	listeners []func(key string, v T, deleted bool)
	ready     chan struct{}
	err       error
}

// Ready is closed when the initial values are loaded.
func (w *Watch[T]) Ready() <-chan struct{} { return w.ready }

// Get returns the current value of key.
func (w *Watch[T]) Get(key string) (T, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	v, ok := w.values[key]
	return v, ok
}

// Len returns the number of keys.
func (w *Watch[T]) Len() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.values)
}

// OnChange registers fn for every later change.
func (w *Watch[T]) OnChange(fn func(key string, v T, deleted bool)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listeners = append(w.listeners, fn)
}

// Err returns why the watch stopped (nil while running or after ctx ended).
func (w *Watch[T]) Err() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.err
}

// watchBucket starts mirroring kv until ctx is done. decode turns a value into T (an error skips
// the entry).
func watchBucket[T any](ctx context.Context, kv jetstream.KeyValue, decode func(key string, value []byte) (T, error)) (*Watch[T], error) {
	kw, err := kv.WatchAll(ctx)
	if err != nil {
		return nil, err
	}
	w := &Watch[T]{values: map[string]T{}, ready: make(chan struct{})}
	go func() {
		defer kw.Stop()
		readyClosed := false
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-kw.Updates():
				if !ok {
					w.mu.Lock()
					w.err = errors.New("stream: watch closed")
					w.mu.Unlock()
					return
				}
				if e == nil {
					if !readyClosed {
						close(w.ready)
						readyClosed = true
					}
					continue
				}
				w.apply(e, decode)
			}
		}
	}()
	return w, nil
}

func (w *Watch[T]) apply(e jetstream.KeyValueEntry, decode func(string, []byte) (T, error)) {
	deleted := e.Operation() != jetstream.KeyValuePut
	var v T
	if !deleted {
		var err error
		if v, err = decode(e.Key(), e.Value()); err != nil {
			return
		}
	}
	w.mu.Lock()
	if deleted {
		v = w.values[e.Key()]
		delete(w.values, e.Key())
	} else {
		w.values[e.Key()] = v
	}
	listeners := w.listeners
	w.mu.Unlock()
	for _, fn := range listeners {
		fn(e.Key(), v, deleted)
	}
}

// WatchTenants mirrors PE_TENANTS (keys are tenant ids).
func (b *Bus) WatchTenants(ctx context.Context) (*Watch[TenantSettings], error) {
	return watchBucket(ctx, b.Tenants, func(_ string, v []byte) (TenantSettings, error) {
		var s TenantSettings
		return s, json.Unmarshal(v, &s)
	})
}

// Erasures is the mirrored set of erasure tombstones.
type Erasures struct{ w *Watch[time.Time] }

// Ready is closed when the initial tombstones are loaded.
func (e *Erasures) Ready() <-chan struct{} { return e.w.Ready() }

// Erased reports whether the player was erased for the tenant, and when.
func (e *Erasures) Erased(tenantID string, player identity.UUID) (time.Time, bool) {
	k, err := ErasureKey(tenantID, player)
	if err != nil {
		return time.Time{}, false
	}
	return e.w.Get(k)
}

// WatchErasures mirrors PE_ERASURES so workers and verdict services drop late data of erased players.
func (b *Bus) WatchErasures(ctx context.Context) (*Erasures, error) {
	w, err := watchBucket(ctx, b.Erasures, func(_ string, v []byte) (time.Time, error) {
		var ms int64
		_, err := fmt.Sscan(string(v), &ms)
		return time.UnixMilli(ms).UTC(), err
	})
	if err != nil {
		return nil, err
	}
	return &Erasures{w: w}, nil
}
