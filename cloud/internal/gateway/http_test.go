package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go/jetstream"
)

func (e *env) httpURL(path string) string {
	return "http://" + e.g.Addr().String() + path
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStatusDocument(t *testing.T) {
	e := newEnv(t, fastOptions(), "PE_REGION=eu-test")
	code, body := get(t, e.httpURL("/status"))
	var st Status
	if code != 200 || json.Unmarshal([]byte(body), &st) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if st.Region != "eu-test" || st.Version != "test" || st.ConnectedServers != 0 || st.ProbeRTT.Samples != 0 || st.SLO.Met != nil ||
		st.SLO.TargetP95Ms != 100 || st.SLO.TargetP99Ms != 250 || !strings.Contains(body, `"met":null`) {
		t.Fatalf("empty status %s", body)
	}

	s := newServer(keyPro)
	c, _ := e.connect(s)
	for i := 0; i < 20; i++ {
		pong(t, c, &pb.Ping{Nonce: uint64(i + 1), LastProbeRttUs: 30_000})
	}
	pong(t, c, &pb.Ping{Nonce: 99, LastProbeRttUs: 400_000}) // 1 of 21 above the p99 target
	e.connect(newServer(keyOther))
	c.Close()
	testkit.Eventually(t, 5*time.Second, "probe stats", func() bool {
		sum, err := e.d.Store.GlobalProbeSummary(context.Background(), store.ProbeSourcePlugin, time.Now().Add(-time.Hour))
		return err == nil && sum.Count == 21
	})
	st, err := BuildStatus(context.Background(), e.d)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConnectedServers < 1 || st.ProbeRTT.Samples != 21 || st.ProbeRTT.P50Us < 30_000 || st.ProbeRTT.P50Us > 40_000 ||
		st.ProbeRTT.P99Us < 400_000 || st.SLO.P95Met == nil || !*st.SLO.P95Met || *st.SLO.P99Met || *st.SLO.Met || st.UptimeS < 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestTrustedProxyEnforcement(t *testing.T) {
	t.Run("untrusted peer refused", func(t *testing.T) {
		e := newEnv(t, fastOptions(), "PE_REQUIRE_TRUSTED_PROXY=true", "PE_TRUSTED_PROXIES=10.0.0.0/8")
		if _, err := testkit.Dial(ctxT(t, 5*time.Second), e.url, nil); err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("dial from an untrusted peer: %v", err)
		}
		if code, _ := get(t, e.httpURL("/status")); code != http.StatusForbidden {
			t.Fatalf("/status %d", code)
		}
		if code, _ := get(t, e.httpURL("/healthz")); code != http.StatusOK {
			t.Fatalf("loopback health checks are answered: %d", code)
		}
	})
	t.Run("client address from the trusted proxy", func(t *testing.T) {
		e := newEnv(t, fastOptions(), "PE_REQUIRE_TRUSTED_PROXY=true", "PE_TRUSTED_PROXIES=127.0.0.0/8")
		via := func(ip string) http.Header { return http.Header{"X-Forwarded-For": {ip}} }
		for i := 0; i < 5; i++ {
			c := e.dial(via("198.51.100.7"))
			if _, err := c.Hello(ctxT(t, 5*time.Second), newServer("bad-key").hello()); err == nil {
				t.Fatal("bad key accepted")
			}
		}
		if _, blocked := e.g.adm.blocked(mustAddr("198.51.100.7"), time.Now()); !blocked {
			t.Fatal("the forwarded client address must be limited")
		}
		if _, blocked := e.g.adm.blocked(mustAddr("127.0.0.1"), time.Now()); blocked {
			t.Fatal("the proxy itself must not be blocked")
		}
		c := e.dial(via("198.51.100.7"))
		var se *testkit.ServerError
		if _, err := c.Hello(ctxT(t, 5*time.Second), newServer(keyPro).hello()); !errors.As(err, &se) || se.Frame.Code != pb.Error_CODE_RATE_LIMITED {
			t.Fatalf("blocked client: %v", err)
		}
		c = e.dial(via("198.51.100.8"))
		if _, err := c.Hello(ctxT(t, 5*time.Second), newServer(keyPro).hello()); err != nil {
			t.Fatalf("another client behind the proxy: %v", err)
		}
	})
}

// TestGracefulShutdown runs the gateway through app.Run like `pandaeye gateway`: on the signal,
// /healthz answers 503, connections get Error{CODE_SHUTTING_DOWN} and close 1001, and their
// PE_CONNECTIONS entries are released.
func TestGracefulShutdown(t *testing.T) {
	d := testkit.NewDeps(t, testEnv()...)
	o := fastOptions()
	o.CloseTimeout = 2 * time.Second
	g, err := NewWithOptions(d, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, d, app.Named{Name: "gateway", Service: g}) }()
	select {
	case <-g.Ready():
	case err := <-done:
		t.Fatal(err)
	}
	e := &env{t: t, d: d, g: g, url: "ws://" + g.Addr().String() + "/v1/connect"}
	if code, _ := get(t, e.httpURL("/healthz")); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	s := newServer(keyPro)
	c, w := e.connect(s)
	key, _ := stream.ServerKey(w.TenantId, s.id)
	watch, err := d.Bus.Connections.Watch(context.Background(), key, jetstream.UpdatesOnly())
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		for u := range watch.Updates() {
			if u != nil && u.Operation() != jetstream.KeyValuePut {
				close(released)
				return
			}
		}
	}()
	start := time.Now()
	cancel()
	f, err := c.Read(ctxT(t, 5*time.Second))
	if err != nil || f.GetError().GetCode() != pb.Error_CODE_SHUTTING_DOWN || !f.GetError().Fatal {
		t.Fatalf("want SHUTTING_DOWN, got %v %v", f, err)
	}
	if r := f.GetError().RetryAfterMs; r < 1000 || r > 5000 {
		t.Fatalf("retry after %d ms", r)
	}
	// The client has not answered the close handshake yet: the gateway is still draining.
	if code, _ := get(t, e.httpURL("/healthz")); code != http.StatusServiceUnavailable {
		t.Fatalf("healthz while shutting down: %d", code)
	}
	if _, err := testkit.Dial(ctxT(t, 5*time.Second), e.url, nil); err == nil {
		t.Fatal("new connections must be refused while draining")
	}
	if _, code := c.ReadErr(ctxT(t, 5*time.Second)); code != 1001 {
		t.Fatalf("close code %d", code)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("connection entry not released")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app.Run: %v", err)
		}
	case <-time.After(app.ShutdownTimeout):
		t.Fatal("shutdown overran")
	}
	if el := time.Since(start); el > app.ServiceStopTimeout {
		t.Fatalf("shutdown took %v", el)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestSecretsNeverLogged runs the gateway at debug level through handshakes, batches, failures and
// reconnects and checks that no licence key, server secret or process key reaches the log.
func TestSecretsNeverLogged(t *testing.T) {
	const key = "SECRET-LICENCE-0123456789"
	d := testkit.NewDeps(t, "PE_STATIC_LICENSES="+key+"::pro", "PE_EVENTS_MAX_BYTES=256MiB")
	var logs lockedBuffer
	d.Log = app.NewLogger(&logs, slog.LevelDebug)
	e := startGateway(t, d, fastOptions())
	s := newServer(key)
	c, _ := e.connect(s)
	sendBatch(t, c, testkit.Batch(1, testkit.NewPlayer("Alice").Join()))
	pong(t, c, &pb.Ping{Nonce: 1, LastProbeRttUs: 1000})
	e.connect(s)
	wrong := *s
	wrong.secret = "SECRET-WRONG-SERVER-SECRET"
	e.rejected(&wrong, pb.Error_CODE_DUPLICATE_SERVER, 4004)
	e.rejected(newServer("SECRET-UNKNOWN-KEY"), pb.Error_CODE_UNAUTHENTICATED, 4001)
	e.stop()
	out := logs.String()
	if !strings.Contains(out, "plugin connected") {
		t.Fatalf("debug logs not captured: %s", out)
	}
	for _, secret := range []string{key, s.secret, wrong.secret, "SECRET-UNKNOWN-KEY", d.Secrets.OutboundKey.Reveal(), d.Secrets.LicensePepper.Reveal()} {
		if strings.Contains(out, secret) {
			t.Fatalf("a secret reached the log: %s", out)
		}
	}
}
