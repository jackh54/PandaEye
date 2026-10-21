// Package checktest runs cloud check detectors over synthetic event sequences, exactly as the worker
// does: View.Apply first, then Detector.Handle, one event at a time.
package checktest

import (
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Run feeds events to a fresh detector of factory f with the default tenant config and returns every
// emission. The view starts empty, so sequences normally begin with a PlayerJoin.
func Run(tb testing.TB, f checks.Factory, events []*pb.Event) []checks.Emission {
	tb.Helper()
	return RunWith(tb, f, events, checks.DefaultConfig{})
}

// RunWith is Run with a tenant check configuration.
func RunWith(tb testing.TB, f checks.Factory, events []*pb.Event, cfg checks.Config) []checks.Emission {
	tb.Helper()
	view := checks.NewView()
	var out []checks.Emission
	ctx := checks.NewCtx(view, cfg, func(e checks.Emission) { out = append(out, e) })
	det := f.New()
	for _, ev := range events {
		view.Apply(ev, 0)
		ctx.SetEvent(ev)
		det.Handle(ctx, ev)
	}
	for _, e := range out {
		ok := false
		for _, id := range f.Checks {
			ok = ok || id == e.Check
		}
		if !ok {
			tb.Fatalf("factory %s emitted undeclared check %q", f.Name, e.Check)
		}
	}
	return out
}

// Count returns how many emissions are for check id.
func Count(es []checks.Emission, id string) int {
	n := 0
	for _, e := range es {
		if e.Check == id {
			n++
		}
	}
	return n
}

// Points sums the points emitted for check id.
func Points(es []checks.Emission, id string) float64 {
	s := 0.0
	for _, e := range es {
		if e.Check == id {
			s += e.Points
		}
	}
	return s
}
