package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/metrics"
)

// Shutdown budget (ARCHITECTURE 5.1: within 10 s of SIGINT/SIGTERM).
const (
	ShutdownTimeout    = 10 * time.Second
	ServiceStopTimeout = 7 * time.Second // services return within this; the rest is for Deps.Close
)

// Named is a service with a name for logs.
type Named struct {
	Name    string
	Service Service
}

// Run runs the services (plus the metrics server when PE_METRICS_LISTEN is set) until ctx is
// cancelled or one of them fails, then cancels the others, waits up to ServiceStopTimeout for them
// and closes d within the overall ShutdownTimeout. It returns the first service error, or an error
// if the shutdown overran.
func Run(ctx context.Context, d *Deps, services ...Named) error {
	if addr := d.Config.MetricsListen; addr != "" {
		srv, err := metrics.NewServer(addr, d.Metrics, d.Log)
		if err != nil {
			d.Close()
			return fmt.Errorf("app: metrics listener: %w", err)
		}
		services = append(services, Named{Name: "metrics", Service: srv})
	}
	// Services are cancelled by us, after Stopping is closed, not directly by ctx: they can tell a
	// shutdown from a failure, and /healthz turns 503 before listeners close.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(services))
	for _, s := range services {
		d.Log.Info("service starting", "service", s.Name)
		go func() {
			defer func() {
				if p := recover(); p != nil {
					results <- result{s.Name, fmt.Errorf("panic: %v", p)}
				}
			}()
			results <- result{s.Name, s.Service.Run(runCtx)}
		}()
	}

	var first error
	pending := len(services)
	select {
	case <-ctx.Done():
		d.Log.Info("shutting down", "reason", context.Cause(ctx))
	case r := <-results:
		pending--
		if r.err != nil {
			first = fmt.Errorf("service %s failed: %w", r.name, r.err)
			d.Log.Error("service failed", "service", r.name, "err", r.err)
		} else {
			d.Log.Info("service stopped", "service", r.name)
		}
	}
	started := time.Now()
	d.markStopping()
	cancel()
	timeout := time.After(ServiceStopTimeout)
	for pending > 0 {
		select {
		case r := <-results:
			pending--
			if r.err != nil && !errors.Is(r.err, context.Canceled) {
				d.Log.Warn("service stopped with error", "service", r.name, "err", r.err)
				if first == nil {
					first = fmt.Errorf("service %s failed: %w", r.name, r.err)
				}
			}
		case <-timeout:
			d.Log.Error("services did not stop in time", "pending", pending)
			first = errors.Join(first, errors.New("app: shutdown timed out"))
			pending = 0
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			d.Log.Warn("closing dependencies", "err", err)
		}
	case <-time.After(ShutdownTimeout - time.Since(started)):
		first = errors.Join(first, errors.New("app: closing dependencies timed out"))
	}
	d.Log.Info("stopped", "took", time.Since(started).Round(time.Millisecond).String(), slog.Bool("clean", first == nil))
	return first
}

// ServiceFunc adapts a function to Service.
type ServiceFunc func(ctx context.Context) error

// Run calls f.
func (f ServiceFunc) Run(ctx context.Context) error { return f(ctx) }
