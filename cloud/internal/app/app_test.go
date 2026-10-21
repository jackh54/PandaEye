package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/store/sqlstore"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

func TestRedactingLogger(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelDebug)
	log.Debug("hello",
		"license_key", "LIC-123-SECRET",
		"Authorization", "Bearer tok-SECRET",
		"service_token", "svc-SECRET",
		"webhook_url", "https://discord.com/api/webhooks/1/hook-SECRET",
		"dsn", "postgres://u:pw-SECRET@db",
		"outbound_key", "key-SECRET",
		"typed", identity.Secret("typed-SECRET"),
		slog.Group("hello", slog.String("server_secret", "grp-SECRET"), slog.String("server_id", "visible-id")),
		"webhook_id", "123456",
		"partition", 7,
	)
	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("secret in log output: %s", out)
	}
	for _, want := range []string{"visible-id", `"webhook_id":"123456"`, `"partition":7`, identity.Redacted} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	for k, want := range map[string]bool{"token": true, "x_key": true, "lease": false, "key": true, "server_id": false, "webhook_id": false, "pepper": true} {
		if SensitiveKey(k) != want {
			t.Errorf("SensitiveKey(%q) = %v", k, !want)
		}
	}
}

func allConfig(t *testing.T, extra ...string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	env := append([]string{
		"PE_DB_DSN=" + filepath.Join(dir, "pe.db"),
		"PE_NATS_DIR=" + filepath.Join(dir, "nats"),
		"PE_EVENTS_MAX_BYTES=256MiB",
		"PE_METRICS_LISTEN=",
		"PE_OWNER_ID=all-test",
	}, extra...)
	cfg, err := config.Load(config.RoleAll, "", env)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func quiet() Options { return Options{Version: "test", Logger: slog.New(slog.DiscardHandler)} }

func TestNewAllModePersistsGeneratedSecrets(t *testing.T) {
	cfg := allConfig(t)
	ctx := context.Background()
	d, err := New(ctx, cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if d.Bus == nil || !d.Bus.Embedded() || d.Store == nil || d.Secrets.OutboundKey.Len() != 32 || d.Secrets.LicensePepper.Len() != 32 {
		t.Fatalf("deps %+v", d.Secrets)
	}
	if d.Secrets.OutboundKey == d.Secrets.LicensePepper {
		t.Fatal("one random value for two secrets")
	}
	key, pepper := d.Secrets.OutboundKey, d.Secrets.LicensePepper
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d2, err := New(ctx, cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if d2.Secrets.OutboundKey != key || d2.Secrets.LicensePepper != pepper {
		t.Fatal("generated secrets must survive a restart")
	}
	configured := allConfig(t, "PE_OUTBOUND_KEY="+strings.Repeat("o", 32))
	d3, err := New(ctx, configured, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer d3.Close()
	if d3.Secrets.OutboundKey.Reveal() != strings.Repeat("o", 32) {
		t.Fatal("configured key must win")
	}
}

func TestNewChecksSchemaOutsideAll(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "pe.db")
	cfg, err := config.Load(config.RoleReplay, "", []string{"PE_DB_DSN=" + dsn})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := New(ctx, cfg, quiet()); !errors.Is(err, sqlstore.ErrSchemaOutdated) {
		t.Fatalf("unmigrated database: %v", err)
	}
	st, err := sqlstore.Open(ctx, "sqlite", dsn, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()
	d, err := New(ctx, cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Bus != nil {
		t.Fatal("replay without PE_NATS_URL must not open a bus")
	}
}

func TestTenantSettingsCacheRebuild(t *testing.T) {
	cfg := allConfig(t)
	ctx := context.Background()
	d, err := New(ctx, cfg, quiet())
	if err != nil {
		t.Fatal(err)
	}
	ten := store.NewTenant(identity.NewTenantID(), "Net", "net", time.Now())
	ten.Enforcement, ten.Leniency = true, 2
	if err := d.Store.CreateTenant(ctx, ten); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.ReplaceTenantChecks(ctx, ten.ID, []store.TenantCheck{{CheckID: "aim.gcd", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Bus.GetTenant(ctx, ten.ID); !errors.Is(err, stream.ErrKeyNotFound) {
		t.Fatalf("unexpected cache entry: %v", err)
	}
	d.Close()

	d, err = New(ctx, cfg, quiet()) // the restart rebuilds the missing entry
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, _, err := d.Bus.GetTenant(ctx, ten.ID)
	if err != nil || !s.Enforcement || s.Leniency != 2 || s.CheckEnabled("aim.gcd", true) || s.Plan != store.PlanFree {
		t.Fatalf("rebuilt settings %+v %v", s, err)
	}
	s.Leniency = 5
	if _, err := d.Bus.PutTenant(ctx, s); err != nil {
		t.Fatal(err)
	}
	if n, err := SyncTenants(ctx, d.Store, d.Bus); err != nil || n != 0 {
		t.Fatalf("sync must not overwrite: %d %v", n, err)
	}
	if got, _, _ := d.Bus.GetTenant(ctx, ten.ID); got.Leniency != 5 {
		t.Fatal("existing entry overwritten")
	}
	if err := PublishTenant(ctx, d.Store, d.Bus, ten.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.Bus.GetTenant(ctx, ten.ID); got.Leniency != 2 {
		t.Fatal("PublishTenant must write the database state")
	}
}

func TestShared(t *testing.T) {
	d := &Deps{}
	calls := 0
	build := func() (any, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("first build fails")
		}
		return calls, nil
	}
	if _, err := d.Shared("x", build); err == nil {
		t.Fatal("error not returned")
	}
	a, _ := d.Shared("x", build)
	b, _ := d.Shared("x", build)
	if a != 2 || b != 2 || calls != 2 {
		t.Fatalf("built %d times: %v %v", calls, a, b)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	d := &Deps{Config: &config.Config{}, Log: slog.New(slog.DiscardHandler)}
	var closed atomic.Bool
	d.OnClose(func() error { closed.Store(true); return nil })
	var stopped atomic.Int32
	svc := ServiceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		stopped.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- Run(ctx, d, Named{"a", svc}, Named{"b", svc}) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-d.Stopping():
		t.Fatal("stopping before shutdown")
	default:
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(ShutdownTimeout):
		t.Fatal("shutdown overran")
	}
	select {
	case <-d.Stopping():
	default:
		t.Fatal("Stopping not closed")
	}
	if stopped.Load() != 2 || !closed.Load() || time.Since(start) > time.Second {
		t.Fatalf("stopped %d closed %v in %v", stopped.Load(), closed.Load(), time.Since(start))
	}
}

func TestRunStopsAllWhenOneFails(t *testing.T) {
	d := &Deps{Config: &config.Config{}, Log: slog.New(slog.DiscardHandler)}
	boom := errors.New("boom")
	var otherStopped atomic.Bool
	err := Run(context.Background(), d,
		Named{"failing", ServiceFunc(func(context.Context) error { return boom })},
		Named{"other", ServiceFunc(func(ctx context.Context) error { <-ctx.Done(); otherStopped.Store(true); return nil })},
		Named{"panicking", ServiceFunc(func(ctx context.Context) error { <-ctx.Done(); panic("bad") })},
	)
	if !errors.Is(err, boom) || !otherStopped.Load() {
		t.Fatalf("Run = %v, other stopped %v", err, otherStopped.Load())
	}
}

func TestRunServesMetrics(t *testing.T) {
	d := &Deps{Config: &config.Config{MetricsListen: "127.0.0.1:0"}, Log: slog.New(slog.DiscardHandler)}
	d.Metrics = metrics.New()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Run(ctx, d); err != nil {
		t.Fatal(err)
	}
}
