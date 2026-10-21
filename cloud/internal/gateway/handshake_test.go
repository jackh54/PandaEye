package gateway

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestHandshakeAndReconnect(t *testing.T) {
	e := newEnv(t, fastOptions())
	ctx := context.Background()
	s := newServer(keyPro)
	c, w := e.connect(s, func(h *pb.Hello) { h.ServerName, h.NetworkId, h.OnlinePlayers = "SMP", "net-1", 7 })
	if w.ServerSecret == "" || !identity.ValidTenantID(w.TenantId) || !identity.ValidServerID(w.ConnectionId) || w.LastAckedSeq != 0 {
		t.Fatalf("welcome %+v", w)
	}
	if w.HeartbeatIntervalMs != 1000 || w.MaxBatchEvents != 4096 || w.MaxEventsPerS != 20_000 || w.MaxBytesPerS != 2<<20 ||
		w.ReplayMaxEventsPerS != 15_000 {
		t.Fatalf("budgets %+v", w)
	}
	if cfg := w.Config; cfg.Leniency != 4 || !cfg.Mitigations || cfg.Enforcement || cfg.Autoban {
		t.Fatalf("config %+v", cfg)
	}
	tenant := w.TenantId
	conn, err := e.connection(tenant, s.id)
	if err != nil || conn.Info.ConnectionID != w.ConnectionId || conn.Info.InstanceID != s.instance || conn.Info.OnlinePlayers != 7 ||
		conn.Info.NetworkID != "net-1" || !conn.Info.ShadowMode || !strings.HasPrefix(conn.Info.GatewayID, "testkit#") {
		t.Fatalf("PE_CONNECTIONS %+v %v", conn, err)
	}
	row, err := e.d.Store.GetServer(ctx, tenant, s.id)
	if err != nil || !identity.CheckSecret(row.SecretHash, s.secret) || row.Name != "SMP" || row.InstanceID != s.instance {
		t.Fatalf("server row %+v %v", row, err)
	}
	rec, _, err := e.d.Bus.GetServerRecord(ctx, tenant, s.id)
	if err != nil || !bytes.Equal(rec.SecretHash, row.SecretHash) {
		t.Fatalf("PE_SERVERS %+v %v", rec, err)
	}

	p := testkit.NewPlayer("Alice")
	sendBatch(t, c, testkit.Batch(1, p.Join(), p.State()))
	sendBatch(t, c, testkit.Batch(2, p.Move(p.Pos, p.Rot)))
	testkit.Eventually(t, 5*time.Second, "acked seq in PE_SERVERS", func() bool {
		r, _, err := e.d.Bus.GetServerRecord(ctx, tenant, s.id)
		return err == nil && r.LastAckedSeq == 2
	})
	c.Close()
	testkit.Eventually(t, 5*time.Second, "connection entry released", func() bool {
		_, err := e.connection(tenant, s.id)
		return errors.Is(err, stream.ErrKeyNotFound)
	})
	testkit.Eventually(t, 5*time.Second, "acked seq in the servers row", func() bool {
		r, err := e.d.Store.GetServer(ctx, tenant, s.id)
		return err == nil && r.LastBatchSeq == 2
	})

	_, w2 := e.connect(s)
	if w2.ServerSecret != "" || w2.TenantId != tenant || w2.LastAckedSeq != 2 || w2.ConnectionId == w.ConnectionId {
		t.Fatalf("reconnect welcome %+v", w2)
	}

	// A free tenant's Welcome advertises the plan's budget.
	_, wf := e.connect(newServer(keyFree))
	if wf.MaxEventsPerS != 3000 || wf.ReplayMaxEventsPerS != 3000 {
		t.Fatalf("free budgets %+v", wf)
	}
}

func TestAuthFailuresBlockTheAddress(t *testing.T) {
	e := newEnv(t, fastOptions())
	for i := 0; i < 4; i++ {
		e.rejected(newServer("no-such-key"), pb.Error_CODE_UNAUTHENTICATED, 4001)
	}
	e.rejected(newServer("malformed key"), pb.Error_CODE_UNAUTHENTICATED, 4001)
	f := e.rejected(newServer(keyPro), pb.Error_CODE_RATE_LIMITED, 4008)
	if f.RetryAfterMs < 3_000_000 {
		t.Fatalf("block of %d ms", f.RetryAfterMs)
	}
	// The block is per address.
	until, blocked := e.g.adm.blocked(mustAddr("127.0.0.2"), time.Now())
	if blocked || until != 0 {
		t.Fatal("another address must not be blocked")
	}
	if _, blocked := e.g.adm.blocked(mustAddr("127.0.0.1"), time.Now()); !blocked {
		t.Fatal("not blocked")
	}
}

func TestHelloValidation(t *testing.T) {
	e := newEnv(t, fastOptions())
	for _, id := range []string{"", "*", ">", "pe.out.>", "3F1C2B4A-0000-4000-8000-000000000001", "00000000-0000-0000-0000-000000000000",
		"3f1c2b4a00004000800000000000000001", "{3f1c2b4a-0000-4000-8000-000000000001}", "3f1c2b4a-0000-4000-8000-00000000000*",
		"t_aaaaaaaaaaaaaaaaaaaa.x", "3f1c2b4a-0000-4000-8000-0000000000011"} {
		s := newServer(keyPro)
		s.id = id
		e.rejected(s, pb.Error_CODE_BAD_REQUEST, 4000)
	}
	e.rejected(newServer(keyPro), pb.Error_CODE_BAD_REQUEST, 4000, func(h *pb.Hello) { h.InstanceId = "not-a-uuid" })
	e.rejected(newServer(keyPro), pb.Error_CODE_UNSUPPORTED_PROTOCOL, 4003, func(h *pb.Hello) { h.ProtocolVersion = 2 })
	if n, _ := e.d.Store.ListTenantIDs(context.Background()); len(n) != 0 {
		t.Fatalf("rejected hellos created tenants: %v", n)
	}
}

func TestProtocolViolations(t *testing.T) {
	o := fastOptions()
	o.HelloTimeout = 300 * time.Millisecond
	e := newEnv(t, o)
	ctx := ctxT(t, 20*time.Second)
	expectBadRequest := func(name string, c *testkit.Client) {
		t.Helper()
		se, code := c.ReadErr(ctx)
		if se == nil || se.Frame.Code != pb.Error_CODE_BAD_REQUEST || code != 4000 {
			t.Errorf("%s: error %v, close %d", name, se, code)
		}
	}
	expectBadRequest("no hello", e.dial(nil))

	c := e.dial(nil)
	c.SendText(ctx, "hello")
	expectBadRequest("text frame", c)

	c = e.dial(nil)
	c.SendBatch(ctx, testkit.Batch(1), codec.Raw)
	expectBadRequest("batch first", c)

	c = e.dial(nil)
	c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: newServer(keyPro).hello()}}, codec.Zstd)
	expectBadRequest("compressed hello", c)

	c = e.dial(nil)
	big := newServer(keyPro).hello()
	big.Capabilities = nil
	big.ServerName = strings.Repeat("x", 200)
	for i := 0; i < 100; i++ {
		big.Capabilities = append(big.Capabilities, strings.Repeat("c", 200))
	}
	c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: big}}, codec.Raw)
	expectBadRequest("oversized hello", c)

	for name, send := range map[string]func(*testkit.Client) error{
		"second hello": func(c *testkit.Client) error {
			return c.Send(ctx, &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: newServer(keyPro).hello()}}, codec.Raw)
		},
		"text after welcome": func(c *testkit.Client) error { return c.SendText(ctx, "x") },
		"unknown codec":      func(c *testkit.Client) error { return c.SendRaw(ctx, []byte{0x07, 1, 2, 3}) },
		"garbage":            func(c *testkit.Client) error { return c.SendRaw(ctx, []byte{0x00, 0xff, 0xff, 0xff}) },
	} {
		c, _ := e.connect(newServer(keyPro))
		if err := send(c); err != nil {
			t.Fatal(err)
		}
		expectBadRequest(name, c)
	}
}

func TestCrossTenantServerIDIsolation(t *testing.T) {
	e := newEnv(t, fastOptions())
	a := newServer(keyPro)
	ca, wa := e.connect(a)
	b := &server{key: keyOther, id: a.id, instance: identity.NewUUID().String()}
	cb, wb := e.connect(b)
	if wb.TenantId == wa.TenantId || wb.ServerSecret == "" || wb.ServerSecret == wa.ServerSecret {
		t.Fatalf("the same server_id under another tenant is another server: %+v %+v", wa, wb)
	}
	for _, x := range []struct {
		tenant string
		c      *testkit.Client
		w      *pb.Welcome
	}{{wa.TenantId, ca, wa}, {wb.TenantId, cb, wb}} {
		conn, err := e.connection(x.tenant, a.id)
		if err != nil || conn.Info.ConnectionID != x.w.ConnectionId {
			t.Fatalf("connection of %s: %+v %v", x.tenant, conn, err)
		}
		sendBatch(t, x.c, testkit.Batch(1, testkit.Probe(1, 1)))
	}
	// Each tenant's secret only opens its own server.
	stolen := &server{key: keyOther, id: a.id, instance: b.instance, secret: a.secret}
	e.rejected(stolen, pb.Error_CODE_DUPLICATE_SERVER, 4004)
	// A verdict for tenant A's server never reaches tenant B's connection.
	publishVerdict(t, e, wa.TenantId, a.id, testVerdict(pb.Action_ACTION_ALERT))
	if v, err := ca.ReadVerdict(ctxT(t, 5*time.Second)); err != nil || v == nil {
		t.Fatalf("tenant A verdict: %v", err)
	}
	expectNoFrame(t, cb, 300*time.Millisecond, func(f *pb.ServerFrame) bool { return f.GetVerdict() != nil })
}

func TestDuplicateInstallRejected(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	c, _ := e.connect(s)
	clone := *s
	clone.instance = identity.NewUUID().String()
	f := e.rejected(&clone, pb.Error_CODE_DUPLICATE_SERVER, 4004)
	if f.RetryAfterMs == 0 {
		t.Fatal("duplicate installs are told to wait")
	}
	if err := c.Ping(ctxT(t, 5*time.Second), &pb.Ping{Nonce: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadUntil(ctxT(t, 5*time.Second), func(f *pb.ServerFrame) bool { return f.GetPong() != nil }); err != nil {
		t.Fatalf("the original connection must survive: %v", err)
	}
	// A wrong or missing secret is a duplicate as well.
	for _, secret := range []string{"", "wrong-secret"} {
		x := *s
		x.secret = secret
		e.rejected(&x, pb.Error_CODE_DUPLICATE_SERVER, 4004)
	}
}

func TestReplacementOfTheSameInstance(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	old, _ := e.connect(s)
	sendBatch(t, old, testkit.Batch(1, testkit.Probe(1, 1)))
	fresh, w := e.connect(s)
	if _, code := old.ReadErr(ctxT(t, 5*time.Second)); code != int(websocket.StatusNormalClosure) {
		t.Fatalf("replaced connection close code %d", code)
	}
	if w.LastAckedSeq != 1 {
		t.Fatalf("last acked %d", w.LastAckedSeq)
	}
	sendBatch(t, fresh, testkit.Batch(2, testkit.Probe(2, 2)))
	time.Sleep(300 * time.Millisecond) // the old session's cleanup must not release the new entry
	conn, err := e.connection(w.TenantId, s.id)
	if err != nil || conn.Info.ConnectionID != w.ConnectionId {
		t.Fatalf("entry %+v %v", conn, err)
	}
}

func TestReplacementOfASilentInstance(t *testing.T) {
	o := fastOptions()
	o.DuplicateSilence = 400 * time.Millisecond
	e := newEnv(t, o)
	s := newServer(keyPro)
	old, _ := e.connect(s)
	time.Sleep(600 * time.Millisecond)
	other := *s
	other.instance = identity.NewUUID().String()
	_, w := e.connect(&other)
	if _, code := old.ReadErr(ctxT(t, 5*time.Second)); code != int(websocket.StatusNormalClosure) {
		t.Fatalf("silent connection close code %d", code)
	}
	conn, err := e.connection(w.TenantId, s.id)
	if err != nil || conn.Info.InstanceID != other.instance {
		t.Fatalf("entry %+v %v", conn, err)
	}
}

func TestReplacementAcrossGateways(t *testing.T) {
	o := fastOptions()
	o.RenewInterval = time.Hour // the old gateway must learn it through close_connection_id
	e1 := newEnv(t, o)
	e2 := startGateway(t, e1.d, o)
	s := newServer(keyPro)
	old, _ := e1.connect(s)
	_, w := e2.connect(s)
	if _, code := old.ReadErr(ctxT(t, 5*time.Second)); code != int(websocket.StatusNormalClosure) {
		t.Fatalf("close code %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	conn, err := e2.connection(w.TenantId, s.id)
	if err != nil || conn.Info.ConnectionID != w.ConnectionId || conn.Info.GatewayID != e2.g.id {
		t.Fatalf("entry %+v %v", conn, err)
	}
}

func TestMaxServers(t *testing.T) {
	e := newEnv(t, fastOptions())
	s1, s2, s3 := newServer(keyTwo), newServer(keyTwo), newServer(keyTwo)
	c1, w := e.connect(s1)
	e.connect(s2)
	e.rejected(s3, pb.Error_CODE_LICENSE_LIMIT, 4002)
	if _, err := e.connection(w.TenantId, s3.id); !errors.Is(err, stream.ErrKeyNotFound) {
		t.Fatalf("refused connection left an entry: %v", err)
	}
	if _, err := e.d.Store.GetServer(context.Background(), w.TenantId, s3.id); err == nil {
		t.Fatal("a refused server must not be registered (it never got its secret)")
	}
	// Replacing a connected server does not count twice.
	e.connect(s2)
	c1.Close()
	testkit.Eventually(t, 5*time.Second, "slot freed", func() bool {
		_, err := e.connection(w.TenantId, s1.id)
		return errors.Is(err, stream.ErrKeyNotFound)
	})
	if _, w3 := e.connect(s3); w3.ServerSecret == "" {
		t.Fatal("third server admitted once a slot is free")
	}
}

func TestConnectionRateLimits(t *testing.T) {
	t.Run("per address", func(t *testing.T) {
		o := fastOptions()
		o.ConnsPerIPPerMin = 3
		e := newEnv(t, o)
		first := newServer(keyPro)
		e.connect(first)
		e.connect(newServer(keyPro))
		e.connect(newServer(keyPro))
		f := e.rejected(newServer(keyPro), pb.Error_CODE_RATE_LIMITED, 4008)
		if f.RetryAfterMs == 0 || f.RetryAfterMs > 20_000 {
			t.Fatalf("retry after %d", f.RetryAfterMs)
		}
		// Reconnects with a valid secret are exempt.
		e.connect(first)
		e.connect(first)
	})
	t.Run("per tenant", func(t *testing.T) {
		o := fastOptions()
		o.HellosPerTenantPerMin = 2
		e := newEnv(t, o)
		e.connect(newServer(keyPro))
		e.connect(newServer(keyPro))
		e.rejected(newServer(keyPro), pb.Error_CODE_RATE_LIMITED, 4008)
		e.connect(newServer(keyOther))
	})
}

// switchVerifier wraps a verifier that can be made unreachable.
type switchVerifier struct {
	inner license.Verifier
	down  atomic.Bool
}

func (v *switchVerifier) Verify(ctx context.Context, key string) (license.License, error) {
	if v.down.Load() {
		return license.License{}, license.ErrUnavailable
	}
	return v.inner.Verify(ctx, key)
}

func TestLicenceVerifierUnavailable(t *testing.T) {
	sv := &switchVerifier{inner: license.NewStatic([]config.StaticLicense{{Key: keyPro, Plan: "pro"}, {Key: keyOther, Plan: "pro"}}, nil)}
	o := fastOptions()
	o.Verifier = sv
	e := newEnv(t, o)
	s := newServer(keyPro)
	c, w := e.connect(s)
	c.Close()
	tenant := e.tenantOf(keyPro)
	if tenant.LicenseValidUntil.Sub(tenant.LicenseVerifiedAt) != license.FallbackValidity {
		t.Fatalf("verification not persisted: %+v", tenant)
	}
	sv.down.Store(true)
	if _, w2 := e.connect(s); w2.TenantId != w.TenantId {
		t.Fatal("fallback to the persisted verification")
	}
	f := e.rejected(newServer(keyOther), pb.Error_CODE_INTERNAL, 1011)
	if f.RetryAfterMs != 30_000 {
		t.Fatalf("retry after %d", f.RetryAfterMs)
	}
	if err := e.d.Store.RecordLicenseCheck(context.Background(), w.TenantId, 0, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.rejected(s, pb.Error_CODE_INTERNAL, 1011)
	if _, blocked := e.g.adm.blocked(mustAddr("127.0.0.1"), time.Now()); blocked {
		t.Fatal("an unreachable verifier is not an auth failure")
	}
}
