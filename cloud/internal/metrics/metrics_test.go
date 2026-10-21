package metrics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServer(t *testing.T) {
	m := New()
	m.ProbeRTT.WithLabelValues(StagePlugin).Observe(0.042)
	m.Batches.WithLabelValues(BatchStored).Inc()
	m.ConsumerLag.WithLabelValues("events-07").Set(3)
	srv, err := NewServer("127.0.0.1:0", m, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	get := func(path string) string {
		resp, err := http.Get("http://" + srv.Addr().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %s", path, resp.Status)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	body := get("/metrics")
	for _, want := range []string{`pandaeye_probe_rtt_seconds_bucket{stage="plugin",le="0.05"} 1`, `pandaeye_gateway_batches_total{result="stored"} 1`,
		`pandaeye_consumer_lag_messages{consumer="events-07"} 3`, "go_goroutines", "process_"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "tenant") {
		t.Error("tenant label exported")
	}
	if !strings.Contains(get("/debug/pprof/"), "goroutine") {
		t.Error("pprof index")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}
