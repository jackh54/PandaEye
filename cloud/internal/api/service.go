package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/netutil"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	"golang.org/x/sync/errgroup"
)

// Limits (ARCHITECTURE 5.8).
const (
	requestsPerKeyPerMinute = 600
	appealsPerIPPerHour     = 5
	licenseFailuresPerIP    = 10 // per 10 minutes
	shutdownGrace           = 5 * time.Second
)

// webhookTester sends the test message when a tenant saves a Discord webhook (*discord.Client).
type webhookTester interface {
	SendTest(ctx context.Context, webhookURL string) error
}

// Service is the api service.
type Service struct {
	d   *app.Deps
	log *slog.Logger
	now func() time.Time

	// Components; setup fills the ones a test did not set.
	verifier license.Verifier
	erasure  ErasureSink // nil: no analytics store (telemetry disabled)
	webhooks webhookTester
	clips    *codec.Decoder

	meterEvery   time.Duration
	janitorEvery time.Duration

	once    sync.Once
	initErr error
	handler http.Handler

	keyLimits    *keyedLimiter
	appealLimits *keyedLimiter
	licenseFails *keyedLimiter

	tenantsMu sync.RWMutex
	tenants   *stream.Watch[stream.TenantSettings] // mirror for metering; nil until Run starts it

	status statusCache
}

var _ app.Service = (*Service)(nil)

// New builds the service from the shared dependencies. Components are created when the service
// first runs or serves (so New succeeds for any Deps).
func New(d *app.Deps) (*Service, error) {
	return &Service{d: d, now: time.Now, meterEvery: time.Minute, janitorEvery: 24 * time.Hour}, nil
}

func (s *Service) setup() error {
	s.once.Do(func() { s.initErr = s.build() })
	return s.initErr
}

func (s *Service) build() error {
	d := s.d
	if d == nil || d.Config == nil || d.Store == nil || d.Bus == nil {
		return errors.New("api: incomplete dependencies (config, store and bus are required)")
	}
	if d.Log == nil {
		s.log = slog.New(slog.DiscardHandler)
	} else {
		s.log = d.Log.With("component", "api")
	}
	var err error
	if s.verifier == nil {
		if s.verifier, err = license.New(d); err != nil {
			return fmt.Errorf("api: licence verifier: %w", err)
		}
	}
	if s.erasure == nil {
		sink, err := telemetry.New(d)
		if err != nil {
			return fmt.Errorf("api: telemetry: %w", err)
		}
		if es, ok := sink.(ErasureSink); ok {
			s.erasure = es
		}
	}
	if s.webhooks == nil {
		c, err := discord.Shared(d)
		if err != nil {
			return fmt.Errorf("api: discord: %w", err)
		}
		s.webhooks = c
	}
	if s.clips == nil {
		if s.clips, err = codec.NewDecoder(codec.Limits{}); err != nil {
			return fmt.Errorf("api: clip decoder: %w", err)
		}
		dec := s.clips
		d.OnClose(func() error { dec.Close(); return nil })
	}
	s.keyLimits = newKeyedLimiter(requestsPerKeyPerMinute, time.Minute)
	s.appealLimits = newKeyedLimiter(appealsPerIPPerHour, time.Hour)
	s.licenseFails = newKeyedLimiter(licenseFailuresPerIP, 10*time.Minute)
	s.handler = s.routes()
	return nil
}

// Handler returns the HTTP handler of the API (tests serve it with httptest).
func (s *Service) Handler() http.Handler {
	if err := s.setup(); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, unavailable("the API is not configured"))
		})
	}
	return s.handler
}

// Run serves PE_API_LISTEN and runs the metering and janitor singletons until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if err := s.setup(); err != nil {
		return err
	}
	cfg := s.d.Config
	ln, err := net.Listen("tcp", cfg.APIListen)
	if err != nil {
		return fmt.Errorf("api: listen %s: %w", cfg.APIListen, err)
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	s.log.Info("api listening", "addr", ln.Addr().String(), "tls", cfg.TLSCert != "")
	g, gctx := errgroup.WithContext(ctx)
	if w, err := s.d.Bus.WatchTenants(gctx); err == nil {
		s.tenantsMu.Lock()
		s.tenants = w
		s.tenantsMu.Unlock()
	} else if gctx.Err() == nil {
		s.log.Warn("tenant settings watch failed; metering reads the database", "err", err)
	}
	g.Go(func() error {
		var err error
		if cfg.TLSCert != "" {
			err = srv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
		} else {
			err = srv.Serve(ln)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("api: serve: %w", err)
	})
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			srv.Close()
		}
		return nil
	})
	g.Go(func() error { return s.runMetering(gctx) })
	g.Go(func() error { return s.runJanitor(gctx) })
	g.Go(func() error { return s.runBackups(gctx) })
	return g.Wait()
}

// stopping reports whether shutdown began (/healthz answers 503 from then on).
func (s *Service) stopping() bool {
	select {
	case <-s.d.Stopping():
		return true
	default:
		return false
	}
}

// withBase wraps every request: PE_REQUIRE_TRUSTED_PROXY, panic recovery, security headers and a
// debug request log that never contains headers, bodies or query strings.
func (s *Service) withBase(next http.Handler) http.Handler {
	cfg := s.d.Config
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.log.Error("api handler panic", "path", r.URL.Path, "panic", fmt.Sprint(p))
				if sw.status == 0 {
					writeError(sw, errf(http.StatusInternalServerError, codeInternal, "internal error"))
				}
			}
			s.log.Debug("api request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"duration_ms", time.Since(start).Milliseconds())
		}()
		h := sw.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		if cfg.RequireTrustedProxy {
			peer, ok := netutil.PeerAddr(r.RemoteAddr)
			if !ok || !cfg.TrustedProxies.PeerAllowed(peer, true) {
				writeError(sw, errf(http.StatusForbidden, codeUntrustedPeer, "connections are accepted only through the trusted proxy"))
				return
			}
		}
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// reqCtx is what an authenticated handler gets.
type reqCtx struct {
	p   *principal
	now time.Time
}

func (rc *reqCtx) tenant() string { return rc.p.tenantID }

type handlerFunc func(w http.ResponseWriter, r *http.Request, rc *reqCtx) error

// handle registers pattern with authentication (acc), the per-key rate limit and error mapping.
func (s *Service) handle(mux *http.ServeMux, pattern string, acc access, fn handlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		now := s.now()
		p, aerr := s.authenticate(r, acc, now)
		if aerr != nil {
			writeError(w, aerr)
			return
		}
		if !acc.public {
			if ok, wait := s.keyLimits.allow(p.limitKey(), now); !ok {
				writeError(w, &apiError{status: http.StatusTooManyRequests, code: codeRateLimited,
					msg: "rate limit exceeded", retryAfter: wait})
				return
			}
		}
		if err := fn(w, r, &reqCtx{p: p, now: now}); err != nil {
			e, expected := asAPIError(err)
			if !expected {
				s.log.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			}
			writeError(w, e)
		}
	})
}

// Route access rules (ARCHITECTURE 5.8; API keys only where a scope is listed).
var (
	accPublic       = access{public: true}
	accService      = access{service: true}
	accRead         = access{role: roleViewer, scope: "read"}
	accStaffVerdict = access{role: roleStaff, scope: "verdicts:write"}
	accStaff        = access{role: roleStaff}
	accAdmin        = access{role: roleAdmin}
	accAdminSetting = access{role: roleAdmin, scope: "settings:write"}
	accErase        = access{role: roleAdmin, scope: "erase"}
	accOwner        = access{role: roleOwner}
)

func (s *Service) routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, acc access, fn handlerFunc) { s.handle(mux, pattern, acc, fn) }

	h("GET /status", accPublic, s.handleStatus)
	h("GET /healthz", accPublic, s.handleHealthz)

	h("POST /api/v1/auth/license", accService, s.handleAuthLicense)
	h("POST /api/v1/auth/rebind", accOwner, s.handleAuthRebind)

	h("GET /api/v1/tenant", accRead, s.handleGetTenant)
	h("PATCH /api/v1/tenant", accAdminSetting, s.handlePatchTenant)
	h("GET /api/v1/servers", accRead, s.handleServers)

	h("GET /api/v1/verdicts", accRead, s.handleListVerdicts)
	h("GET /api/v1/verdicts/{id}", accRead, s.handleGetVerdict)
	h("POST /api/v1/verdicts/{id}/confirm", accStaffVerdict, s.handleConfirm)
	h("POST /api/v1/verdicts/{id}/overturn", accStaffVerdict, s.handleOverturn)
	h("GET /api/v1/clips/{id}", accRead, s.handleClip)

	h("GET /api/v1/players", accRead, s.handleListPlayers)
	h("GET /api/v1/players/{uuid}", accRead, s.handleGetPlayer)
	h("DELETE /api/v1/players/{uuid}", accErase, s.handleErasePlayer)
	h("POST /api/v1/players/{uuid}/watch", accStaff, s.handleWatch)

	h("GET /api/v1/bans", accRead, s.handleBans)
	h("GET /api/v1/appeals", accRead, s.handleListAppeals)
	h("POST /api/v1/appeals/{id}/decide", accStaff, s.handleDecideAppeal)
	h("POST /api/v1/public/appeals", accService, s.handlePublicAppeal)

	h("GET /api/v1/stats/overview", accRead, s.handleOverview)
	h("GET /api/v1/stats/latency", accRead, s.handleLatency)
	h("GET /api/v1/usage", accRead, s.handleUsage)

	h("GET /api/v1/apikeys", accOwner, s.handleListAPIKeys)
	h("POST /api/v1/apikeys", accOwner, s.handleCreateAPIKey)
	h("DELETE /api/v1/apikeys/{id}", accOwner, s.handleRevokeAPIKey)

	h("GET /api/v1/checks", accAdminSetting, s.handleGetChecks)
	h("PUT /api/v1/checks", accAdminSetting, s.handlePutChecks)
	h("GET /api/v1/audit", accAdmin, s.handleAudit)
	h("GET /api/v1/export/labels", accRead, s.handleExportLabels)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			if m == r.Method {
				continue
			}
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pattern := mux.Handler(probe); pattern != "/" && pattern != "" {
				writeError(w, errf(http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed"))
				return
			}
		}
		writeError(w, notFound("route"))
	})
	return s.withBase(mux)
}

func (s *Service) handleHealthz(w http.ResponseWriter, _ *http.Request, _ *reqCtx) error {
	if s.stopping() {
		return unavailable("shutting down")
	}
	writeJSON(w, http.StatusOK, okJSON{OK: true})
	return nil
}
