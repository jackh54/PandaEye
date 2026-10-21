package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/config"
)

func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", what)
	}
}

// Builds register closers (telemetry, ml) and build other shared values (license.resolver builds
// license); neither may deadlock.
func TestSharedBuildMayReenter(t *testing.T) {
	d := &Deps{}
	within(t, "OnClose inside a build", func() {
		d.Shared("telemetry", func() (any, error) { d.OnClose(func() error { return nil }); return 1, nil })
	})
	within(t, "nested Shared", func() {
		v, err := d.Shared("license.resolver", func() (any, error) {
			return d.Shared("license", func() (any, error) { return 2, nil })
		})
		if v != 2 || err != nil {
			t.Errorf("nested = %v, %v", v, err)
		}
	})
	if len(d.closers) != 1 {
		t.Fatalf("closers = %d", len(d.closers))
	}
}

func TestStoppingClosedBeforeServicesAreCancelled(t *testing.T) {
	for i := 0; i < 20; i++ {
		d := &Deps{Log: NewLogger(discard{}, nil), Config: &config.Config{}}
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
		go func() { time.Sleep(2 * time.Millisecond); cancel() }()
		Run(ctx, d, Named{Name: "svc", Service: svc})
		if !<-seen {
			t.Fatal("a service saw cancellation before Stopping was closed")
		}
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
