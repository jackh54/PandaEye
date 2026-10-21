package stream

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

var testTimings = LeaseTimings{TTL: 2 * time.Second, Renew: 250 * time.Millisecond, Fence: 1200 * time.Millisecond, Settle: 500 * time.Millisecond, Request: 400 * time.Millisecond}

func testLogger(t testing.TB) *slog.Logger {
	if os.Getenv("PE_TEST_VERBOSE") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})).With("test", t.Name())
	}
	return slog.New(slog.DiscardHandler)
}

// startServer runs a JetStream server on a loopback port so several buses (owners) can share it.
func startServer(t testing.TB) *server.Server {
	t.Helper()
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), MaxPayload: MaxPayload, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("server not ready")
	}
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	return srv
}

func testConfig(url, owner string) Config {
	return Config{URL: url, Name: owner, Partitions: 4, OwnerID: owner, Lease: testTimings, PublishTimeout: time.Second, EventsMaxBytes: 256 << 20}
}

func openBus(t testing.TB, cfg Config) *Bus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := Open(ctx, cfg, testLogger(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func ctxT(t testing.TB, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startSmallPayloadServer runs a server with the default 1 MiB max_payload.
func startSmallPayloadServer(t *testing.T) (string, error) {
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoSigs: true, NoLog: true})
	if err != nil {
		return "", err
	}
	srv.Start()
	srv.ReadyForConnections(10 * time.Second)
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL(), nil
}
