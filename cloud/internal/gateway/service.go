package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// Service is the gateway service.
type Service struct {
	d    *app.Deps
	opts Options
	id   string // gateway id in PE_CONNECTIONS values

	log      *slog.Logger
	m        *gwMetrics
	resolver *license.Resolver
	dec      *codec.Decoder
	tenants  *stream.Watch[stream.TenantSettings]
	adm      *admission
	budgets  *tenantBudgets
	risk     *riskBudgets
	status   *statusCache

	ready chan struct{}
	addr  net.Addr

	mu       sync.Mutex
	draining bool
	sessions map[string]*session // by connection id
	wg       sync.WaitGroup      // one per HTTP connection being upgraded or served
}

var _ app.Service = (*Service)(nil)

// New builds the service from the shared dependencies. Everything that needs the dependencies is
// set up by Run.
func New(d *app.Deps) (*Service, error) { return NewWithOptions(d, Options{}) }

// NewWithOptions is New with tuned options (tests).
func NewWithOptions(d *app.Deps, o Options) (*Service, error) {
	return &Service{d: d, opts: o.withDefaults(), ready: make(chan struct{}), sessions: map[string]*session{}}, nil
}

// Ready is closed once Run listens; Addr is valid from then on.
func (s *Service) Ready() <-chan struct{} { return s.ready }

// Addr returns the listening address (after Ready).
func (s *Service) Addr() net.Addr { return s.addr }

func (s *Service) init(ctx context.Context) error {
	d := s.d
	if d.Config == nil || d.Store == nil || d.Bus == nil {
		return errors.New("gateway: needs configuration, store and event bus")
	}
	s.log = d.Log.With("component", "gateway")
	s.m = newGWMetrics(d.Metrics)
	owner := d.Config.OwnerID
	if owner == "" {
		owner = "gateway"
	}
	s.id = owner + "#" + identity.NewUUID().String()[:8]
	v := s.opts.Verifier
	if v == nil {
		var err error
		if v, err = license.New(d); err != nil {
			return err
		}
	}
	var err error
	if s.resolver, err = license.NewResolver(v, d.Store, d.Bus, d.Secrets.LicensePepper, s.log); err != nil {
		return err
	}
	if s.dec, err = codec.NewDecoder(codec.Limits{MaxBatchEvents: s.opts.MaxBatchEvents}); err != nil {
		return err
	}
	if s.tenants, err = d.Bus.WatchTenants(ctx); err != nil {
		s.dec.Close()
		return fmt.Errorf("gateway: watch PE_TENANTS: %w", err)
	}
	s.tenants.OnChange(s.tenantChanged)
	s.adm = newAdmission(s.opts)
	s.budgets = newTenantBudgets(s.opts.PlanEventsPerS)
	s.risk = newRiskBudgets(d.Store)
	s.status = newStatusCache(s)
	return nil
}

// Run serves until ctx is cancelled, then closes every connection with CODE_SHUTTING_DOWN.
func (s *Service) Run(ctx context.Context) error {
	if err := s.init(ctx); err != nil {
		return err
	}
	defer s.dec.Close()
	select {
	case <-s.tenants.Ready():
	case <-time.After(10 * time.Second):
		s.log.Warn("PE_TENANTS not loaded yet; using tenant rows until it is")
	case <-ctx.Done():
		return nil
	}
	addr := s.opts.Listen
	if addr == "" {
		addr = s.d.Config.GatewayListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway: listen: %w", err)
	}
	s.addr = ln.Addr()
	// IdleTimeout: keep-alive connections of plain requests (403s, /healthz) must not stay open
	// forever; hijacked WebSockets are not affected.
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10,
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug)}
	errc := make(chan error, 1)
	go func() {
		if c := s.d.Config; c.TLSCert != "" {
			errc <- srv.ServeTLS(ln, c.TLSCert, c.TLSKey)
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	bg, stopBG := context.WithCancel(context.Background())
	defer stopBG()
	go s.housekeeping(bg)
	s.log.Info("gateway listening", "addr", s.addr.String(), "gateway_id", s.id, "tls", s.d.Config.TLSCert != "")
	close(s.ready)
	select {
	case err := <-errc:
		s.drain()
		return fmt.Errorf("gateway: serve: %w", err)
	case <-ctx.Done():
	}
	s.drain()
	sctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		srv.Close()
	}
	return nil
}

// drain refuses new connections and closes the open ones with CODE_SHUTTING_DOWN (retry after a
// jittered 1-5 s), releasing their PE_CONNECTIONS entries, within DrainTimeout. /healthz answers 503
// from the start.
func (s *Service) drain() {
	s.mu.Lock()
	s.draining = true
	open := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		open = append(open, ss)
	}
	s.mu.Unlock()
	for _, ss := range open {
		retry := uint32(1000 + rand.IntN(4000))
		ss.terminate("shutdown", fatal(pb.Error_CODE_SHUTTING_DOWN, "gateway shutting down", retry))
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.opts.DrainTimeout):
		s.log.Warn("connections still closing at the end of the drain", "open", s.sessionCount())
	}
}

func (s *Service) stopping() bool {
	select {
	case <-s.d.Stopping():
		return true
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// track registers an HTTP connection about to be upgraded; false while draining.
func (s *Service) track() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *Service) addSession(ss *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return false
	}
	s.sessions[ss.id] = ss
	return true
}

func (s *Service) removeSession(ss *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[ss.id] == ss {
		delete(s.sessions, ss.id)
	}
}

func (s *Service) session(connectionID string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[connectionID]
}

func (s *Service) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Service) tenantSessions(tenantID string) []*session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*session
	for _, ss := range s.sessions {
		if ss.ready.Load() && ss.tenantID == tenantID {
			out = append(out, ss)
		}
	}
	return out
}

// settings returns the tenant's current settings from PE_TENANTS, or def (from the tenant row)
// while the bucket has no entry.
func (s *Service) settings(tenantID string, def stream.TenantSettings) stream.TenantSettings {
	if v, ok := s.tenants.Get(tenantID); ok {
		return v
	}
	return def
}

// tenantChanged pushes a changed RemoteConfig to the tenant's connections at once (every Pong
// carries it too).
func (s *Service) tenantChanged(key string, v stream.TenantSettings, deleted bool) {
	if deleted {
		return
	}
	rc := v.RemoteConfig()
	for _, ss := range s.tenantSessions(key) {
		if prev := ss.lastConfig.Load(); prev != nil && proto.Equal(prev, rc) {
			continue
		}
		ss.lastConfig.Store(rc)
		ss.enqueue(outItem{frame: &pb.ServerFrame{Kind: &pb.ServerFrame_ConfigUpdate{ConfigUpdate: rc}}, kind: "config_update"}, false)
	}
}

// evict closes the connection a new one replaced: directly when it is on this gateway, otherwise
// by asking its gateway with Outbound.close_connection_id.
func (s *Service) evict(tenantID, serverID, connectionID string) {
	if ss := s.session(connectionID); ss != nil {
		ss.terminate("replaced", nil)
		return
	}
	out := &pb.Outbound{TenantId: tenantID, ServerId: serverID, Kind: &pb.Outbound_CloseConnectionId{CloseConnectionId: connectionID}}
	if err := s.d.Bus.PublishOutbound(s.d.Secrets.OutboundKey.Bytes(), out); err != nil {
		s.log.Warn("asking the old gateway to close a replaced connection failed", "tenant", tenantID, "server", serverID, "err", err)
	}
}

func (s *Service) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.adm.prune(now)
			s.budgets.prune(now)
			s.risk.prune(now)
		}
	}
}

// tenantDefaults converts a tenant row into settings for tenants not yet in PE_TENANTS.
func tenantDefaults(t *store.Tenant) stream.TenantSettings { return app.TenantSettings(t, nil) }
