package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler serves /metrics for the registry and the pprof endpoints under /debug/pprof/.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry}))
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Server serves Handler on an address (PE_METRICS_LISTEN). It implements the app Service contract.
type Server struct {
	addr string
	m    *Metrics
	log  *slog.Logger
	ln   net.Listener
}

// NewServer listens on addr immediately (so a busy port fails at start) and serves on Run.
func NewServer(addr string, m *Metrics, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{addr: addr, m: m, log: log, ln: ln}, nil
}

// Addr returns the bound address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Run serves until ctx is cancelled, then shuts down within 2 s.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Handler: s.m.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(s.ln) }()
	s.log.Info("metrics listening", "addr", s.ln.Addr().String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
