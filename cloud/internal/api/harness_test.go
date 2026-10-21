package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/license"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// Secrets of the test environment (the log test asserts none of them is ever logged).
const (
	testServiceToken     = "svc-token-0123456789abcdef0123456789abcdef"
	testServiceTokenNext = "svc-token-NEXT-0123456789abcdef0123456789ab"
	testAssertionKey     = "assertion-key-0123456789abcdef0123456789abc"
	testAssertionKeyNext = "assertion-key-NEXT-0123456789abcdef01234567"
	testOutboundKey      = "outbound-key-0123456789abcdef0123456789abcd"
	testPepper           = "license-pepper-0123456789abcdef0123456789ab"
	testLicenseKey       = "LICENSE-KEY-SECRET-1234"
	testWebhookToken     = "Tok3n_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	testWebhookURL       = "https://discord.com/api/webhooks/123456789012345678/" + testWebhookToken
)

// The fixed clock of every test environment.
// It is far from the real time, so the gateway's /status document (which reads the real clock)
// never sees the fixtures' probe stats.
var fixedNow = time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

// Tenants.
const (
	tenantA = "t_aaaaaaaaaaaaaaaaaaaa"
	tenantB = "t_bbbbbbbbbbbbbbbbbbbb"
	tenantC = "t_cccccccccccccccccccc"
)

type fakeVerifier struct {
	mu    sync.Mutex
	valid map[string]license.License
	err   error
	calls int
}

func (f *fakeVerifier) Verify(_ context.Context, key string) (license.License, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return license.License{}, f.err
	}
	return f.valid[key], nil
}

type fakeSink struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeSink) ErasePlayer(_ context.Context, tenantID, player string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tenantID+"/"+player)
	return f.err
}

type fakeWebhooks struct {
	mu    sync.Mutex
	sent  []string
	err   error
	block bool
}

func (f *fakeWebhooks) SendTest(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, url)
	return f.err
}

func (f *fakeWebhooks) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type env struct {
	t        *testing.T
	d        *app.Deps
	s        *Service
	srv      *httptest.Server
	logs     *syncBuffer
	verifier *fakeVerifier
	sink     *fakeSink
	webhooks *fakeWebhooks
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newEnv starts an API on a `pandaeye all` style Deps (sqlite, embedded NATS) with tenants A, B
// and C, a fixed clock and fake verifier, analytics sink and webhook tester. Logs go to e.logs at
// debug level.
func newEnv(t *testing.T, extraEnv ...string) *env {
	t.Helper()
	base := []string{
		"PE_SERVICE_TOKEN=" + testServiceToken, "PE_SERVICE_TOKEN_NEXT=" + testServiceTokenNext,
		"PE_ASSERTION_KEY=" + testAssertionKey, "PE_ASSERTION_KEY_NEXT=" + testAssertionKeyNext,
		"PE_OUTBOUND_KEY=" + testOutboundKey, "PE_LICENSE_PEPPER=" + testPepper,
		"PE_PUBLIC_URL=https://dash.example.test", "PE_REGION=test-region",
	}
	d := testkit.NewDeps(t, append(base, extraEnv...)...)
	logs := &syncBuffer{}
	d.Log = app.NewLogger(io.MultiWriter(logs, testLogWriter{t}), slog.LevelDebug)
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, d: d, s: s, logs: logs, verifier: &fakeVerifier{valid: map[string]license.License{}},
		sink: &fakeSink{}, webhooks: &fakeWebhooks{}}
	s.now = func() time.Time { return fixedNow }
	s.verifier, s.erasure, s.webhooks = e.verifier, e.sink, e.webhooks
	e.srv = httptest.NewServer(s.Handler())
	t.Cleanup(e.srv.Close)
	ctx := context.Background()
	for _, tn := range []struct{ id, name, plan string }{{tenantA, "Alpha", store.PlanFree}, {tenantB, "Beta", "pro"}, {tenantC, "Gamma", "pro"}} {
		tt := store.NewTenant(tn.id, tn.name, strings.ToLower(tn.name), fixedNow.Add(-90*24*time.Hour))
		tt.Plan = tn.plan
		if err := d.Store.CreateTenant(ctx, tt); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	if os.Getenv("PE_TEST_VERBOSE") != "" {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// claims signs a dashboard assertion.
func sign(key string, c assertionClaims) string {
	body, _ := json.Marshal(c)
	b := base64.RawURLEncoding.EncodeToString(body)
	mac := identity.HMACSHA256([]byte(key), []byte(b))
	return b + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// reqOpt configures one request.
type reqOpt func(r *http.Request)

// as signs the request for a dashboard user of tenant with role.
func as(tenant, role string) reqOpt {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testServiceToken)
		r.Header.Set("X-PandaEye-Assertion", sign(testAssertionKey, assertionClaims{Tenant: tenant, UserID: "u_" + role,
			Username: role + "-user", Role: role, Method: r.Method, Path: r.URL.EscapedPath(), Exp: fixedNow.Add(45 * time.Second).Unix()}))
	}
}

func withClaims(key string, c assertionClaims) reqOpt {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testServiceToken)
		r.Header.Set("X-PandaEye-Assertion", sign(key, c))
	}
}

func bearer(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func service() reqOpt { return bearer(testServiceToken) }

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("response is not a JSON object: %v: %s", err, r.body)
	}
	return m
}

func (r resp) code(t *testing.T) string {
	t.Helper()
	m := r.json(t)
	if _, ok := m["error"].(string); !ok {
		t.Fatalf("error body without message: %s", r.body)
	}
	c, _ := m["code"].(string)
	return c
}

func (e *env) do(method, path string, body any, opts ...reqOpt) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			data, err := json.Marshal(b)
			if err != nil {
				e.t.Fatal(err)
			}
			rd = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-PandaEye-Client-IP", "198.51.100.7")
	for _, o := range opts {
		o(req)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp{status: res.StatusCode, body: data, header: res.Header}
}

// must performs a request and fails unless it answered want.
func (e *env) must(want int, method, path string, body any, opts ...reqOpt) resp {
	e.t.Helper()
	r := e.do(method, path, body, opts...)
	if r.status != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.status, want, r.body)
	}
	return r
}

// apiKey creates an API key with scopes for tenant directly in the store and returns its token.
func (e *env) apiKey(tenant string, scopes ...string) (id, token string) {
	e.t.Helper()
	id, secret, tok := identity.NewAPIKey()
	if err := e.d.Store.CreateAPIKey(context.Background(), &store.APIKey{ID: id, TenantID: tenant, Name: "k",
		KeyHash: identity.HashSecret(secret.Reveal()), Scopes: scopes, CreatedAt: fixedNow}); err != nil {
		e.t.Fatal(err)
	}
	return id, tok.Reveal()
}

// appealCodeFor is the appeal code of a verdict under the test outbound key.
func appealCodeFor(verdictID string) string {
	return identity.AppealCode([]byte(testOutboundKey), verdictID)
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// connect registers a live connection in PE_CONNECTIONS.
func (e *env) connect(tenant, server string, info stream.ConnectionInfo) {
	e.t.Helper()
	if info.ConnectionID == "" {
		info.ConnectionID = identity.NewUUID().String()
	}
	_, err := e.d.Bus.CreateConnection(context.Background(), tenant, server, info)
	if err != nil && !errors.Is(err, stream.ErrKeyExists) {
		e.t.Fatal(err)
	}
}

// outbox captures SignedOutbound messages of a tenant.
type outbox struct {
	t      *testing.T
	sub    *nats.Subscription
	tenant string
}

func (e *env) outbox(tenant string) *outbox {
	e.t.Helper()
	sub, err := e.d.Bus.NC.SubscribeSync("pe.out." + tenant + ".*")
	if err != nil {
		e.t.Fatal(err)
	}
	check(e.t, e.d.Bus.NC.Flush())
	e.t.Cleanup(func() { sub.Unsubscribe() })
	return &outbox{t: e.t, sub: sub, tenant: tenant}
}

// next returns the next outbound (decoded and signature-checked) or nil after wait.
func (o *outbox) next(wait time.Duration) *pb.Outbound {
	o.t.Helper()
	m, err := o.sub.NextMsg(wait)
	if err != nil {
		return nil
	}
	_, server, _ := strings.Cut(strings.TrimPrefix(m.Subject, "pe.out."), ".")
	out, err := stream.OutboundFor(m.Data, o.tenant, server, []byte(testOutboundKey))
	if err != nil {
		o.t.Fatalf("outbound on %s does not verify: %v", m.Subject, err)
	}
	return out
}

func (o *outbox) all(wait time.Duration) []*pb.Outbound {
	var out []*pb.Outbound
	for {
		m := o.next(wait)
		if m == nil {
			return out
		}
		out = append(out, m)
	}
}

// verdictRecords reads every VerdictRecord published for tenant.
func (e *env) verdictRecords(tenant string) []*pb.VerdictRecord {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subj, _ := stream.VerdictsSubject(tenant)
	c, err := e.d.Bus.JS.OrderedConsumer(ctx, stream.StreamVerdicts, jetstream.OrderedConsumerConfig{FilterSubjects: []string{subj}})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []*pb.VerdictRecord
	for {
		batch, err := c.Fetch(100, jetstream.FetchMaxWait(300*time.Millisecond))
		if err != nil {
			e.t.Fatal(err)
		}
		n := 0
		for m := range batch.Messages() {
			n++
			var r pb.VerdictRecord
			check(e.t, proto.Unmarshal(m.Data(), &r))
			out = append(out, &r)
		}
		if n == 0 {
			return out
		}
	}
}

// ---- golden files -------------------------------------------------------------------------------

// golden compares a JSON response with testdata/golden/<name>.json after replacing the values of
// the given keys (anywhere in the document) with "<volatile>".
func golden(t *testing.T, name string, body []byte, volatile ...string) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: not JSON: %v: %s", name, err, body)
	}
	got := canonical(t, normalize(v, volatile))
	path := filepath.Join("testdata", "golden", name+".json")
	if *update {
		check(t, os.MkdirAll(filepath.Dir(path), 0o755))
		check(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run go test -run %s -update)", name, err, t.Name())
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Errorf("%s differs from %s:\n got: %s\nwant: %s", name, path, got, want)
	}
}

func normalize(v any, volatile []string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if contains(volatile, k) && y != nil {
				x[k] = "<volatile>"
				continue
			}
			x[k] = normalize(y, volatile)
		}
	case []any:
		for i := range x {
			x[i] = normalize(x[i], volatile)
		}
	}
	return v
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func canonical(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// keys asserts that obj has exactly the listed keys (docs/API.md shapes); optional keys may be
// absent.
func keys(t *testing.T, what string, obj any, required []string, optional ...string) {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Fatalf("%s: not an object: %v", what, obj)
	}
	var got []string
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: missing key %q (has %v)", what, k, got)
		}
	}
	for _, k := range got {
		if !contains(required, k) && !contains(optional, k) {
			t.Errorf("%s: unexpected key %q", what, k)
		}
	}
}

// Shapes of docs/API.md.
var (
	verdictKeys = []string{"id", "server_id", "network_id", "player_uuid", "player_name", "session_id", "action", "category",
		"score", "confidence", "reason", "checks", "shadow", "status", "supersedes", "created_at", "delivered_at", "acked_at",
		"executed", "ack_note", "clip_ids", "mitigations", "strength"}
	banKeys = []string{"verdict_id", "server_id", "player_uuid", "active", "reason", "created_at", "overturned_at",
		"overturned_by", "overturn_note", "unban_verdict_id", "unban_state"}
	appealKeys = []string{"id", "verdict_id", "player_uuid", "message", "contact", "status", "created_at", "decided_at",
		"decided_by", "decision_note"}
	tenantKeys = []string{"id", "name", "slug", "plan", "leniency", "autoban", "autokick", "mitigations", "enforcement",
		"reputation_opt_in", "discord_webhook", "discord_min_action", "over_limit_since"}
	playerKeys = []string{"uuid", "last_name", "first_seen_at", "last_seen_at", "last_server_id"}
)
