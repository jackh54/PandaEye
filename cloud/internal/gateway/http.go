package gateway

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/netutil"
)

// Handler serves /v1/connect, /healthz and /status (Run serves it; /metrics stays on the metrics
// listener).
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/connect", s.serveConnect)
	mux.HandleFunc("GET /healthz", s.serveHealthz)
	mux.HandleFunc("GET /status", s.status.serve)
	return s.trustedPeers(mux)
}

// trustedPeers implements PE_REQUIRE_TRUSTED_PROXY: only trusted proxies may connect (403
// otherwise). Loopback health checks are always answered.
func (s *Service) trustedPeers(next http.Handler) http.Handler {
	cfg := s.d.Config
	if !cfg.RequireTrustedProxy {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := netutil.PeerAddr(r.RemoteAddr)
		if !ok || (!cfg.TrustedProxies.PeerAllowed(peer, true) && !(r.URL.Path == "/healthz" && peer.IsLoopback())) {
			s.m.ConnectionsClosed.WithLabelValues("untrusted_peer").Inc()
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) serveHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.stopping() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

func (s *Service) serveConnect(w http.ResponseWriter, r *http.Request) {
	if s.stopping() || !s.track() {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	defer s.wg.Done()
	ip, _ := s.d.Config.TrustedProxies.RequestClientIP(r)
	if wait, ok := s.adm.enter(ip, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		w.Header().Set("Connection", "close")
		http.Error(w, "too many connections from this address", http.StatusTooManyRequests)
		return
	}
	defer s.adm.leave(ip)
	hc := &hijackCapture{ResponseWriter: w}
	// Plugins are not browsers and authenticate in the Hello, so the Origin check does not apply.
	conn, err := websocket.Accept(hc, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	// Until the Welcome only a Hello may arrive: bound what an unauthenticated peer can make us buffer
	// (with headroom, so an oversized Hello still gets its BAD_REQUEST); raised after the Welcome.
	conn.SetReadLimit(4*codec.MaxHello + 1)
	newSession(s, conn, hc.conn, ip).run()
}

// hijackCapture keeps the TCP connection websocket.Accept hijacks, so a peer that stalls the close
// handshake can be dropped at once.
type hijackCapture struct {
	http.ResponseWriter
	conn net.Conn
}

func (h *hijackCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := h.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("gateway: connection cannot be hijacked")
	}
	c, rw, err := hj.Hijack()
	h.conn = c
	return c, rw, err
}
