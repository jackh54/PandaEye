package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"golang.org/x/time/rate"
)

const (
	writeTimeout = 10 * time.Second
	auxDrain     = 2 * time.Second // queued side work still runs this long after the session ended
)

// session is one plugin WebSocket.
type session struct {
	g    *Service
	conn *websocket.Conn
	raw  net.Conn // the hijacked TCP connection
	ip   netip.Addr
	id   string // connection id
	log  *slog.Logger

	ctx    context.Context // ends when the session terminates
	cancel context.CancelFunc

	// Set by the handshake, read-only afterwards.
	tenantID, serverID string
	hello              *pb.Hello
	tenantDef          stream.TenantSettings // tenant row at the Hello, until PE_TENANTS has an entry
	ready              atomic.Bool           // Welcome sent
	sub                *nats.Subscription
	budget             connBudget

	kvRev      atomic.Uint64 // PE_CONNECTIONS revision; 0 = not registered
	online     atomic.Uint32
	lastRecv   atomic.Int64 // unix ns of the last frame received
	readSince  atomic.Int64 // unix ns since the reader waits on the socket (0 = holding a message)
	lastSend   atomic.Int64
	lastConfig atomic.Pointer[pb.RemoteConfig]

	msgs chan inbound
	out  chan outItem
	aux  chan func(context.Context)

	opened      time.Time // the WebSocket was accepted
	connectedMs int64     // set by register

	// Owned by the processing loop.
	offsetUs   int64
	haveOffset bool

	seq    *seqAdvancer
	probes probeRecorder

	deliveredMu sync.Mutex
	delivered   map[string]time.Time // verdict id -> last delivery on this connection

	workers   sync.WaitGroup
	closeOnce sync.Once
	closed    chan struct{} // the WebSocket is closed

	reasonMu    sync.Mutex
	reason      string // why it closed (metrics label); the first reason wins
	reasonAttrs []any  // what the close log line adds to the reason
}

type inbound struct {
	typ  websocket.MessageType
	data []byte
	at   time.Time
}

// outItem is one queued outbound frame; verdict items are re-validated by the writer.
type outItem struct {
	frame   *pb.ServerFrame
	verdict *pb.Verdict
	kind    string // metrics label
}

func newSession(g *Service, conn *websocket.Conn, raw net.Conn, ip netip.Addr) *session {
	ctx, cancel := context.WithCancel(context.Background())
	id := identity.NewUUID().String()
	s := &session{g: g, conn: conn, raw: raw, ip: ip, id: id, ctx: ctx, cancel: cancel,
		log: g.log.With("connection", id), opened: time.Now(),
		msgs: make(chan inbound), out: make(chan outItem, g.opts.OutboundQueue), aux: make(chan func(context.Context), 256),
		delivered: map[string]time.Time{}, closed: make(chan struct{}), budget: newConnBudget(g.opts)}
	s.seq = newSeqAdvancer(s)
	now := time.Now().UnixNano()
	s.lastRecv.Store(now)
	s.lastSend.Store(now)
	return s
}

// run serves the connection until it closes.
func (s *session) run() {
	if !s.g.addSession(s) {
		s.terminate("shutdown", fatal(pb.Error_CODE_SHUTTING_DOWN, "gateway shutting down", 1000))
		<-s.closed
		return
	}
	defer s.cleanup()
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("connection handler panicked", "panic", p, "stack", string(debug.Stack()))
			s.terminate("internal", fatal(pb.Error_CODE_INTERNAL, "internal error", 5_000))
		}
	}()
	go s.readLoop()
	if !s.handshake() {
		return
	}
	s.g.m.Connections.Inc()
	defer s.g.m.Connections.Dec()
	s.start(s.writeLoop)
	s.start(s.auxLoop)
	s.start(s.tickLoop)
	s.start(s.seq.loop)
	s.deliverPending(true)
	s.processLoop()
}

func (s *session) start(fn func()) {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		fn()
	}()
}

// readLoop reads messages and hands them over one at a time: while the processing loop holds a
// message (publishing, or waiting for budget tokens), nothing more is read from the socket.
func (s *session) readLoop() {
	defer close(s.msgs)
	for {
		s.readSince.Store(time.Now().UnixNano())
		typ, data, err := s.conn.Read(context.Background())
		s.readSince.Store(0)
		if err != nil {
			if s.ctx.Err() == nil {
				reason, attrs := readCloseReason(err)
				s.closeReason(reason, attrs...)
			}
			return
		}
		now := time.Now()
		s.lastRecv.Store(now.UnixNano())
		select {
		case s.msgs <- inbound{typ: typ, data: data, at: now}:
		case <-s.ctx.Done():
			return
		}
	}
}

// readCloseReason classifies the error that ended the read loop: the peer's close frame (its code and
// reason go to the close log), a message over the read limit, a write that timed out, or a broken
// connection (only the error's kind is logged: its text carries addresses).
func readCloseReason(err error) (string, []any) {
	var ce websocket.CloseError
	switch {
	case errors.As(err, &ce):
		attrs := []any{"close_code", int(ce.Code), "close_reason", ce.Reason}
		switch ce.Code {
		case websocket.StatusNormalClosure, websocket.StatusGoingAway:
			return "client", attrs
		case websocket.StatusMessageTooBig:
			return "message_too_big", attrs
		default:
			return "client_error", attrs
		}
	case errors.Is(err, websocket.ErrMessageTooBig):
		return "message_too_big", nil
	case errors.Is(err, net.ErrClosed):
		// While the session runs, only a timed-out write closes the socket (the library does), and
		// the reader may see that before the writer reports it.
		return "write_failed", []any{"io_error", "timeout"}
	default:
		return "connection_lost", []any{"io_error", ioErrorKind(err)}
	}
}

func (s *session) processLoop() {
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.terminate("client", nil)
				return
			}
			if !s.handle(m) {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// handle processes one message; false ends the session.
func (s *session) handle(m inbound) bool {
	if m.typ != websocket.MessageBinary {
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "only binary messages are allowed", 0))
		return false
	}
	s.g.m.BatchBytes.Add(float64(len(m.data)))
	if !s.waitBudget("bytes", s.budget.bytes, len(m.data)) {
		return false
	}
	f, err := s.g.dec.Decode(s.ctx, m.data)
	if err != nil {
		return s.undecodable(m, err)
	}
	switch k := f.Kind.(type) {
	case *pb.ClientFrame_Batch:
		return s.handleBatch(k.Batch, m.at)
	case *pb.ClientFrame_Ping:
		return s.handlePing(k.Ping, m.at)
	case *pb.ClientFrame_VerdictAck:
		s.handleVerdictAck(k.VerdictAck, m.at)
		return true
	case *pb.ClientFrame_Goodbye:
		s.log.Debug("plugin said goodbye", "reason", k.Goodbye.Reason)
		s.closeReason("goodbye")
		return true
	case *pb.ClientFrame_Hello:
		s.fail(fatal(pb.Error_CODE_BAD_REQUEST, "second hello", 0))
		return false
	default:
		return true // unknown kinds from newer plugins are ignored
	}
}

// waitBudget waits for n tokens of lim; false when the session ended meanwhile.
func (s *session) waitBudget(budget string, lim *rate.Limiter, n int) bool {
	waited, err := wait(s.ctx, lim, n)
	if waited > time.Millisecond {
		s.g.m.backpressure.WithLabelValues(budget).Add(waited.Seconds())
	}
	return err == nil
}

// fail sends a fatal error and closes with its close code.
func (s *session) fail(e *pb.Error) {
	if !s.ready.Load() {
		s.g.m.Auth.WithLabelValues(authResult(e.Code)).Inc()
	}
	s.terminate(authResult(e.Code), e)
}

// closeReason records why the connection ends and what the close log line adds to it (first reason
// wins).
func (s *session) closeReason(r string, attrs ...any) {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	if s.reason == "" {
		s.reason, s.reasonAttrs = r, attrs
	}
}

func (s *session) closedReason() (string, []any) {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	return s.reason, s.reasonAttrs
}

// writeFailed ends the session after a failed write.
func (s *session) writeFailed(err error) {
	s.closeReason("write_failed", "io_error", ioErrorKind(err))
	s.terminate("write_failed", nil)
}

// terminate ends the session: optionally sends a fatal Error, then closes the WebSocket with the
// error's close code (normal closure without one). A peer that does not complete the close handshake
// within CloseTimeout is dropped. Safe to call from any goroutine, any number of times.
func (s *session) terminate(reason string, e *pb.Error) {
	s.closeOnce.Do(func() {
		code, text := websocket.StatusNormalClosure, reason
		if e != nil {
			code, text = closeCode(e.Code), e.Code.String()
			s.closeReason(reason, "error_code", text, "error_message", e.Message)
		} else {
			s.closeReason(reason)
		}
		s.cancel()
		go func() {
			defer close(s.closed)
			if e != nil {
				s.write(errorFrame(e))
			}
			done := make(chan struct{})
			go func() {
				s.conn.Close(code, text)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(s.g.opts.CloseTimeout):
				if s.raw != nil {
					s.raw.Close()
				}
			}
		}()
	})
}

// write sends one frame now (the library serialises concurrent writers).
func (s *session) write(f *pb.ServerFrame) error {
	msg, err := codec.EncodeServerFrame(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
		return err
	}
	s.lastSend.Store(time.Now().UnixNano())
	return nil
}

// enqueue queues an outbound frame. With mustDeliver a full queue closes the connection so the
// plugin reconnects and pending verdicts are re-sent; otherwise the frame is dropped.
func (s *session) enqueue(it outItem, mustDeliver bool) {
	select {
	case s.out <- it:
	default:
		if mustDeliver {
			s.g.m.Drops.WithLabelValues("outbound_overflow").Inc()
			s.fail(fatal(pb.Error_CODE_INTERNAL, "outbound queue overflow", 1000))
			return
		}
		s.g.m.Drops.WithLabelValues("outbound_" + it.kind).Inc()
	}
}

// runAux queues slow side work (acks, player lookups, store updates) off the processing loop.
func (s *session) runAux(what string, fn func(context.Context)) {
	select {
	case s.aux <- fn:
	default:
		s.g.m.Drops.WithLabelValues("aux_" + what).Inc()
	}
}

// auxLoop runs the queued side work. Jobs still queued when the session ends (verdict acks above
// all) run within auxDrain; their contexts are cancelled then.
func (s *session) auxLoop() {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	stop := context.AfterFunc(s.ctx, func() { time.AfterFunc(auxDrain, cancelBase) })
	defer stop()
	run := func(fn func(context.Context)) {
		ctx, cancel := context.WithTimeout(base, 10*time.Second)
		defer cancel()
		fn(ctx)
	}
	for {
		select {
		case fn := <-s.aux:
			run(fn)
		case <-s.ctx.Done():
			for {
				select {
				case fn := <-s.aux:
					run(fn)
				default:
					return
				}
			}
		}
	}
}

// cleanup runs after the processing loop ended: it stops the workers, flushes the acked seq and
// probe statistics, releases the PE_CONNECTIONS entry and waits for the WebSocket to close.
func (s *session) cleanup() {
	s.terminate("closed", nil)
	if s.sub != nil {
		s.sub.Unsubscribe()
	}
	s.workers.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if s.ready.Load() {
		s.seq.flush(ctx, true)
		s.probes.flush(ctx, s)
	}
	if rev := s.kvRev.Load(); rev != 0 {
		if err := s.g.d.Bus.DeleteConnection(ctx, s.tenantID, s.serverID, rev); err != nil && !isRevisionErr(err) {
			s.log.Debug("releasing the connection entry failed", "err", err)
		}
	}
	s.g.removeSession(s)
	select {
	case <-s.closed:
	case <-time.After(s.g.opts.CloseTimeout + time.Second):
	}
	reason, details := s.closedReason()
	if s.ready.Load() {
		s.g.m.ConnectionsClosed.WithLabelValues(reason).Inc()
	}
	s.logClosed(reason, details)
}

// logClosed logs how the connection ended, in one line: at info once the Hello's licence was
// checked, at debug before (anyone can open such connections).
func (s *session) logClosed(reason string, details []any) {
	lvl := slog.LevelDebug
	var attrs []any
	if h := s.hello; h != nil {
		lvl = slog.LevelInfo
		if s.tenantID == "" {
			attrs = append(attrs, "server", h.ServerId) // the licence was refused: no tenant
		}
		attrs = append(attrs, "platform", h.Platform.String())
	}
	attrs = append(attrs, "cause", closeCause(reason), "reason", reason)
	attrs = append(attrs, details...)
	attrs = append(attrs, "duration", time.Since(s.opened).Round(time.Millisecond).String())
	s.log.Log(context.Background(), lvl, "connection closed", attrs...)
}

// settings returns the tenant's current settings.
func (s *session) settings() stream.TenantSettings { return s.g.settings(s.tenantID, s.tenantDef) }

func (s *session) remoteConfig() *pb.RemoteConfig {
	rc := s.settings().RemoteConfig()
	s.lastConfig.Store(rc)
	return rc
}
