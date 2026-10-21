package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Static licences of every test deployment.
const (
	keyPro     = "test-key-pro"     // unlimited servers, plan pro (no tenant event budget)
	keyTwo     = "test-key-two"     // 2 servers, plan pro
	keyFree    = "test-key-free"    // plan free (3 000 events/s per tenant)
	keyOther   = "test-key-other"   // another tenant, plan pro
	staticKeys = keyPro + "::pro," + keyTwo + ":2:pro," + keyFree + "::free," + keyOther + "::pro"
)

// env is a running gateway on a `pandaeye all` dependency set.
type env struct {
	t   *testing.T
	d   *app.Deps
	g   *Service
	url string
}

// fastOptions are short timings for tests.
func fastOptions() Options {
	return Options{HelloTimeout: 2 * time.Second, HeartbeatInterval: time.Second, RenewInterval: 200 * time.Millisecond,
		SweepInterval: 200 * time.Millisecond, ResendAfter: 400 * time.Millisecond, CloseTimeout: 500 * time.Millisecond,
		DrainTimeout: 3 * time.Second, Listen: "127.0.0.1:0"}
}

func newEnv(t *testing.T, o Options, extraEnv ...string) *env {
	t.Helper()
	d := testkit.NewDeps(t, append([]string{"PE_STATIC_LICENSES=" + staticKeys}, extraEnv...)...)
	return startGateway(t, d, o)
}

// startGateway runs a gateway on d until the test ends.
func startGateway(t *testing.T, d *app.Deps, o Options) *env {
	t.Helper()
	if o.Listen == "" {
		o.Listen = "127.0.0.1:0"
	}
	g, err := NewWithOptions(d, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	select {
	case <-g.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("gateway: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("gateway did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("gateway Run: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("gateway did not stop")
		}
	})
	return &env{t: t, d: d, g: g, url: "ws://" + g.Addr().String() + "/v1/connect"}
}

func ctxT(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func (e *env) dial(header http.Header) *testkit.Client {
	e.t.Helper()
	c, err := testkit.Dial(ctxT(e.t, 10*time.Second), e.url, header)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.CloseNow() })
	return c
}

// server is one simulated plugin install.
type server struct {
	key, id, instance, secret string
}

func newServer(key string) *server {
	return &server{key: key, id: testkit.NewServerID(), instance: identity.NewUUID().String()}
}

func (s *server) hello() *pb.Hello {
	h := testkit.Hello(s.key, s.id)
	h.InstanceId, h.ServerSecret = s.instance, s.secret
	return h
}

// connect performs the handshake, keeping the issued secret, and fails the test unless it succeeds.
func (e *env) connect(s *server, mut ...func(*pb.Hello)) (*testkit.Client, *pb.Welcome) {
	e.t.Helper()
	c, w, err := e.try(s, mut...)
	if err != nil {
		e.t.Fatalf("hello: %v", err)
	}
	return c, w
}

func (e *env) try(s *server, mut ...func(*pb.Hello)) (*testkit.Client, *pb.Welcome, error) {
	e.t.Helper()
	c := e.dial(nil)
	h := s.hello()
	for _, f := range mut {
		f(h)
	}
	w, err := c.Hello(ctxT(e.t, 10*time.Second), h)
	if err == nil && w.ServerSecret != "" {
		s.secret = w.ServerSecret
	}
	return c, w, err
}

// rejected performs a handshake that must fail with code and close code.
func (e *env) rejected(s *server, code pb.Error_Code, closeCode int, mut ...func(*pb.Hello)) *pb.Error {
	e.t.Helper()
	c, _, err := e.try(s, mut...)
	var se *testkit.ServerError
	if !errors.As(err, &se) || se.Frame.Code != code || !se.Frame.Fatal {
		e.t.Fatalf("want fatal %v, got %v", code, err)
	}
	if _, cc := c.ReadErr(ctxT(e.t, 10*time.Second)); cc != closeCode {
		e.t.Fatalf("want close code %d, got %d", closeCode, cc)
	}
	return se.Frame
}

func (e *env) tenantOf(key string) *store.Tenant {
	e.t.Helper()
	l, err := e.d.Store.LookupLicense(context.Background(), identity.LicenseKeyHMAC(e.d.Secrets.LicensePepper.Bytes(), key))
	if err != nil {
		e.t.Fatal(err)
	}
	t, err := e.d.Store.GetTenant(context.Background(), l.TenantID)
	if err != nil {
		e.t.Fatal(err)
	}
	return t
}

// patchTenant changes tenant settings in the database and PE_TENANTS, as the API does, and waits
// until the gateway's mirror has the change.
func (e *env) patchTenant(tenantID string, p store.TenantPatch) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.d.Store.UpdateTenant(ctx, tenantID, p, time.Now()); err != nil {
		e.t.Fatal(err)
	}
	if err := app.PublishTenant(ctx, e.d.Store, e.d.Bus, tenantID); err != nil {
		e.t.Fatal(err)
	}
	want, _, err := e.d.Bus.GetTenant(ctx, tenantID)
	if err != nil {
		e.t.Fatal(err)
	}
	testkit.Eventually(e.t, 5*time.Second, "tenant settings mirrored", func() bool {
		got, ok := e.g.tenants.Get(tenantID)
		return ok && got.UpdatedUnixMs == want.UpdatedUnixMs && got.Enforcement == want.Enforcement &&
			got.Autoban == want.Autoban && got.ReputationOptIn == want.ReputationOptIn
	})
}

func (e *env) connection(tenantID, serverID string) (stream.Connection, error) {
	return e.d.Bus.GetConnection(context.Background(), tenantID, serverID)
}

func ptr[T any](v T) *T { return &v }

// sendBatch sends a batch and waits for its ack.
func sendBatch(t *testing.T, c *testkit.Client, b *pb.Batch) {
	t.Helper()
	ctx := ctxT(t, 15*time.Second)
	if err := c.SendBatch(ctx, b, codec.Zstd); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitBatchAck(ctx, b.Seq); err != nil {
		t.Fatal(err)
	}
}
