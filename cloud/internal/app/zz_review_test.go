package app

import (
	"context"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"testing"
	"time"
)

func TestReviewSharedBuildCallsOnClose(t *testing.T) {
	d := &Deps{shared: map[string]any{}}
	done := make(chan error, 1)
	go func() {
		_, err := d.Shared("telemetry", func() (any, error) {
			d.OnClose(func() error { return nil }) // a component registering its flush on shutdown
			return 1, nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		t.Logf("returned: %v", err)
	case <-time.After(2 * time.Second):
		t.Logf("DEADLOCK: Shared(build) with OnClose inside build never returned")
	}
}

func TestReviewSharedNested(t *testing.T) {
	d := &Deps{shared: map[string]any{}}
	done := make(chan struct{})
	go func() {
		d.Shared("license.resolver", func() (any, error) {
			return d.Shared("license", func() (any, error) { return 1, nil })
		})
		close(done)
	}()
	select {
	case <-done:
		t.Log("nested Shared returned")
	case <-time.After(2 * time.Second):
		t.Log("DEADLOCK: nested Shared (different keys) never returned")
	}
}

func TestReviewStoppingOrder(t *testing.T) {
	before := 0
	for i := 0; i < 50; i++ {
		d := &Deps{shared: map[string]any{}, Log: NewLogger(discard{}, nil), Config: &config.Config{}}
		ctx, cancel := context.WithCancel(context.Background())
		seen := make(chan bool, 1)
		svc := ServiceFunc(func(ctx context.Context) error {
			<-ctx.Done()
			select {
			case <-d.Stopping():
				seen <- true
			default:
				seen <- false
			}
			return nil
		})
		go func() { time.Sleep(5 * time.Millisecond); cancel() }()
		Run(ctx, d, Named{Name: "svc", Service: svc})
		if <-seen {
			before++
		}
	}
	t.Logf("Stopping() already closed when the service saw ctx.Done in %d/50 runs", before)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
