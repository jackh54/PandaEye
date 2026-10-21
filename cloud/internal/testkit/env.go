package testkit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/store/sqlstore"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
)

// FastLeases are lease timings for tests (same ratios as production, ~7x faster).
var FastLeases = stream.LeaseTimings{TTL: 2 * time.Second, Renew: 250 * time.Millisecond, Fence: 1200 * time.Millisecond,
	Settle: 300 * time.Millisecond, Request: 400 * time.Millisecond}

// Logger returns a logger writing to the test log (debug level with PE_TEST_VERBOSE set, else
// warnings only), with the production redaction.
func Logger(t testing.TB) *slog.Logger {
	level := slog.LevelWarn
	if os.Getenv("PE_TEST_VERBOSE") != "" {
		level = slog.LevelDebug
	}
	return app.NewLogger(testWriter{t}, level)
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// NewStore returns a migrated sqlite store in a temporary directory.
func NewStore(t testing.TB) *sqlstore.Store {
	t.Helper()
	ctx := context.Background()
	s, err := sqlstore.Open(ctx, sqlstore.DriverSQLite, filepath.Join(t.TempDir(), "pe.db"), sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// NewBus returns a bus on its own embedded server in a temporary directory, with FastLeases.
// tune may adjust the configuration (partitions default to 4).
func NewBus(t testing.TB, tune ...func(*stream.Config)) *stream.Bus {
	t.Helper()
	cfg := stream.Config{Dir: t.TempDir(), Partitions: 4, OwnerID: "test", Lease: FastLeases, PublishTimeout: time.Second}
	for _, f := range tune {
		f(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := stream.Open(ctx, cfg, Logger(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// NewDeps returns the dependencies of a `pandaeye all` process (sqlite and embedded NATS in a
// temporary directory, FastLeases, 4 partitions, metrics server off). env adds PE_* settings.
func NewDeps(t testing.TB, env ...string) *app.Deps {
	t.Helper()
	dir := t.TempDir()
	base := []string{
		"PE_DB_DSN=" + filepath.Join(dir, "pe.db"),
		"PE_NATS_DIR=" + filepath.Join(dir, "nats"),
		"PE_PARTITIONS=4",
		"PE_METRICS_LISTEN=",
		"PE_OWNER_ID=testkit",
	}
	cfg, err := config.Load(config.RoleAll, "", append(base, env...))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := app.New(ctx, cfg, app.Options{Version: "test", Logger: Logger(t), HTTPClient: offline(),
		TuneStream: func(c *stream.Config) { c.Lease, c.PublishTimeout = FastLeases, time.Second }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// offline is an HTTP client that never reaches the network (Cloudflare refresh falls back).
func offline() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
