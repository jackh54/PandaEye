package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestReadCloseReason(t *testing.T) {
	reset := &net.OpError{Op: "read", Net: "tcp", Source: &net.TCPAddr{IP: net.IPv4(10, 1, 2, 3), Port: 8080},
		Addr: &net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 50000}, Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	closeFrame := func(code websocket.StatusCode, reason string) error {
		return fmt.Errorf("failed to get reader: %w", fmt.Errorf("received close frame: %w", websocket.CloseError{Code: code, Reason: reason}))
	}
	for _, tc := range []struct {
		err    error
		reason string
		attrs  []any
	}{
		{closeFrame(websocket.StatusNormalClosure, ""), "client", []any{"close_code", 1000, "close_reason", ""}},
		{closeFrame(websocket.StatusGoingAway, "edge restart"), "client", []any{"close_code", 1001, "close_reason", "edge restart"}},
		{closeFrame(4100, "custom"), "client_error", []any{"close_code", 4100, "close_reason", "custom"}},
		{closeFrame(websocket.StatusMessageTooBig, ""), "message_too_big", []any{"close_code", 1009, "close_reason", ""}},
		{fmt.Errorf("failed to read: %w: read limited at 65537 bytes", websocket.ErrMessageTooBig), "message_too_big", nil},
		{fmt.Errorf("failed to get reader: failed to read frame header: %w", io.EOF), "connection_lost", []any{"io_error", "eof"}},
		{fmt.Errorf("failed to read frame payload: %w", io.ErrUnexpectedEOF), "connection_lost", []any{"io_error", "eof"}},
		{fmt.Errorf("failed to get reader: failed to read frame header: %w", reset), "connection_lost", []any{"io_error", "reset"}},
		{errors.New("received unmasked frame from client"), "connection_lost", []any{"io_error", "other"}},
		{fmt.Errorf("failed to get reader: %w", net.ErrClosed), "write_failed", []any{"io_error", "timeout"}},
	} {
		reason, attrs := readCloseReason(tc.err)
		if reason != tc.reason || fmt.Sprint(attrs) != fmt.Sprint(tc.attrs) {
			t.Errorf("readCloseReason(%v) = %s %v, want %s %v", tc.err, reason, attrs, tc.reason, tc.attrs)
		}
		if strings.Contains(fmt.Sprint(attrs), "203.0.113.7") {
			t.Errorf("an address reached the attributes: %v", attrs)
		}
	}
}

// A write that times out makes the library close the socket, and the read loop, which may see that
// first, must still blame the write.
func TestWriteTimeoutSeenByTheReader(t *testing.T) {
	readErr, writeErr := make(chan error, 1), make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			_, _, err := c.Read(context.Background())
			readErr <- err
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		writeErr <- c.Write(ctx, websocket.MessageBinary, make([]byte, 64<<20)) // the client never reads
	}))
	defer srv.Close()
	c, _, err := websocket.Dial(ctxT(t, 5*time.Second), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	select {
	case err := <-writeErr:
		if ioErrorKind(err) != "timeout" {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write did not time out")
	}
	select {
	case err := <-readErr:
		if reason, attrs := readCloseReason(err); reason != "write_failed" || fmt.Sprint(attrs) != "[io_error timeout]" {
			t.Fatalf("read %v: %s %v", err, reason, attrs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read did not end")
	}
}

func TestIOErrorKind(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", err)}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{io.EOF, "eof"},
		{fmt.Errorf("failed to read frame header: %w", io.ErrUnexpectedEOF), "eof"},
		{fmt.Errorf("failed to write msg: %w", op(syscall.ECONNRESET)), "reset"},
		{fmt.Errorf("failed to write msg: %w", op(syscall.EPIPE)), "broken_pipe"},
		{fmt.Errorf("failed to write msg: %w", context.DeadlineExceeded), "timeout"},
		{&net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}, "timeout"},
		{fmt.Errorf("failed to write msg: %w", net.ErrClosed), "closed"},
		{errors.New("something else"), "other"},
	} {
		if got := ioErrorKind(tc.err); got != tc.want {
			t.Errorf("ioErrorKind(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestCloseCause(t *testing.T) {
	for reason, want := range map[string]string{
		"client": "peer_close", "client_error": "peer_close", "goodbye": "peer_close",
		"connection_lost": "read_error", "write_failed": "write_error", "silent": "silent_timeout", "replaced": "replaced",
		"shutdown": "shutdown", "invalid": "refused", "limit": "refused", "duplicate": "refused", "rate_limited": "refused",
		"bad_request": "protocol_error", "message_too_big": "protocol_error", "unavailable": "internal_error",
		"internal": "internal_error", "closed": "internal_error",
	} {
		if got := closeCause(reason); got != want {
			t.Errorf("closeCause(%s) = %s, want %s", reason, got, want)
		}
	}
	// Every fatal Error the gateway sends has a cause of its own.
	for code, want := range map[pb.Error_Code]string{
		pb.Error_CODE_BAD_REQUEST: "protocol_error", pb.Error_CODE_UNAUTHENTICATED: "refused", pb.Error_CODE_LICENSE_LIMIT: "refused",
		pb.Error_CODE_UNSUPPORTED_PROTOCOL: "protocol_error", pb.Error_CODE_DUPLICATE_SERVER: "refused",
		pb.Error_CODE_RATE_LIMITED: "refused", pb.Error_CODE_INTERNAL: "internal_error", pb.Error_CODE_SHUTTING_DOWN: "shutdown",
	} {
		if got := closeCause(authResult(code)); got != want {
			t.Errorf("%v: cause %s, want %s", code, got, want)
		}
	}
}

// closeLines returns the "connection closed" log lines.
func closeLines(t *testing.T, logs *lockedBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(l, `"msg":"connection closed"`) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// closeLine waits for the one close line matching match.
func closeLine(t *testing.T, logs *lockedBuffer, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	var found []map[string]any
	testkit.Eventually(t, 10*time.Second, what+" logged", func() bool {
		found = found[:0]
		for _, m := range closeLines(t, logs) {
			if match(m) {
				found = append(found, m)
			}
		}
		return len(found) > 0
	})
	time.Sleep(50 * time.Millisecond)
	found = found[:0]
	for _, m := range closeLines(t, logs) {
		if match(m) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: %d close lines: %v", what, len(found), found)
	}
	return found[0]
}

func byConnection(id string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["connection"] == id }
}

// wantClose checks a close line's fields; a nil value means the field must be absent.
func wantClose(t *testing.T, what string, m map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		got, ok := m[k]
		if v == nil {
			if ok {
				t.Errorf("%s: %s = %v, want none (%v)", what, k, got, m)
			}
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("%s: %s = %v, want %v (%v)", what, k, got, v, m)
		}
	}
	if d, err := time.ParseDuration(fmt.Sprint(m["duration"])); err != nil || d < 0 {
		t.Errorf("%s: duration %v", what, m["duration"])
	}
}

// TestConnectionCloseLogged ends connections in every way and checks that each one logs one line
// saying who ended it, without the peer's address.
func TestConnectionCloseLogged(t *testing.T) {
	d := testkit.NewDeps(t, testEnv()...)
	var logs lockedBuffer
	d.Log = app.NewLogger(&logs, slog.LevelDebug)
	o := fastOptions()
	e := startGateway(t, d, o)
	paper := pb.Platform_PLATFORM_PAPER.String()

	// The peer closes: normally, going away with a reason, with a code of its own, after a Goodbye.
	for _, tc := range []struct {
		name   string
		close  func(*testkit.Client) error
		reason string
		code   any
		text   any
	}{
		{"normal", func(c *testkit.Client) error { return c.Close() }, "client", 1000, ""},
		{"going away", func(c *testkit.Client) error { return c.CloseWith(1001, "edge restart") }, "client", 1001, "edge restart"},
		{"own code", func(c *testkit.Client) error { return c.CloseWith(4100, "custom") }, "client_error", 4100, "custom"},
		{"goodbye", func(c *testkit.Client) error {
			if err := c.Goodbye(ctxT(t, 5*time.Second), "plugin stopping"); err != nil {
				return err
			}
			pong(t, c, &pb.Ping{Nonce: 9}) // the Goodbye was handled before the close frame is read
			return c.CloseWith(1001, "plugin stopping")
		}, "goodbye", nil, nil},
	} {
		s := newServer(keyPro)
		c, w := e.connect(s)
		_ = tc.close(c)
		m := closeLine(t, &logs, tc.name, byConnection(w.ConnectionId))
		wantClose(t, tc.name, m, map[string]any{"level": "INFO", "tenant": w.TenantId, "server": s.id, "platform": paper,
			"cause": "peer_close", "reason": tc.reason, "close_code": tc.code, "close_reason": tc.text, "io_error": nil})
	}

	// The connection breaks without a close frame.
	s := newServer(keyPro)
	c, w := e.connect(s)
	c.CloseNow()
	m := closeLine(t, &logs, "dropped", byConnection(w.ConnectionId))
	wantClose(t, "dropped", m, map[string]any{"level": "INFO", "cause": "read_error", "reason": "connection_lost", "close_code": nil})
	if k := m["io_error"]; k != "eof" && k != "reset" {
		t.Errorf("dropped: io_error %v", k)
	}

	// A reconnect replaces the connection (the new one logs "replacing the previous connection").
	old, w0 := e.connect(s)
	fresh, w1 := e.connect(s)
	old.ReadErr(ctxT(t, 5*time.Second))
	m = closeLine(t, &logs, "replaced", byConnection(w0.ConnectionId))
	wantClose(t, "replaced", m, map[string]any{"level": "INFO", "tenant": w0.TenantId, "server": s.id, "cause": "replaced", "reason": "replaced"})
	if !strings.Contains(logs.String(), `"msg":"replacing the previous connection"`) {
		t.Error("the new connection did not log the replacement")
	}

	// Silence: no frame for three heartbeat intervals.
	m = closeLine(t, &logs, "silent", byConnection(w1.ConnectionId))
	wantClose(t, "silent", m, map[string]any{"level": "INFO", "cause": "silent_timeout", "reason": "silent"})
	if d, err := time.ParseDuration(fmt.Sprint(m["silent_for"])); err != nil || d < 3*o.HeartbeatInterval {
		t.Errorf("silent_for %v", m["silent_for"])
	}
	fresh.CloseNow()

	// Refusals: over the licence's server limit, and a licence that does not verify.
	s1, s2, s3 := newServer(keyTwo), newServer(keyTwo), newServer(keyTwo)
	c1, w2 := e.connect(s1)
	c2, _ := e.connect(s2)
	e.rejected(s3, pb.Error_CODE_LICENSE_LIMIT, 4002)
	m = closeLine(t, &logs, "limit", func(m map[string]any) bool { return m["server"] == s3.id })
	wantClose(t, "limit", m, map[string]any{"level": "INFO", "tenant": w2.TenantId, "platform": paper, "cause": "refused", "reason": "limit",
		"error_code": "CODE_LICENSE_LIMIT", "error_message": "the licence allows 2 connected servers"})
	c1.Close()
	c2.Close()
	bad := newServer("not-a-licence")
	e.rejected(bad, pb.Error_CODE_UNAUTHENTICATED, 4001)
	m = closeLine(t, &logs, "invalid licence", func(m map[string]any) bool { return m["server"] == bad.id })
	wantClose(t, "invalid licence", m, map[string]any{"level": "INFO", "tenant": nil, "platform": paper, "cause": "refused", "reason": "invalid",
		"error_code": "CODE_UNAUTHENTICATED"})

	// Before a Hello anyone can connect: such closes stay at debug.
	e.dial(nil).Close()
	closeLine(t, &logs, "no hello", func(m map[string]any) bool { return m["level"] == "DEBUG" && m["reason"] == "client" })

	// Shutdown closes the open connections.
	_, w3 := e.connect(newServer(keyPro))
	e.stop()
	m = closeLine(t, &logs, "shutdown", byConnection(w3.ConnectionId))
	wantClose(t, "shutdown", m, map[string]any{"level": "INFO", "cause": "shutdown", "reason": "shutdown", "error_code": "CODE_SHUTTING_DOWN"})

	for _, m := range closeLines(t, &logs) {
		if b, _ := json.Marshal(m); strings.Contains(string(b), "127.0.0.1") {
			t.Errorf("an address reached the close log: %s", b)
		}
	}
}
