package checks

import (
	"fmt"
	"math"
	"sort"
	"sync"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Detector is the per-player instance of one cloud check family. The worker creates one Detector per
// player session from every registered Factory and calls Handle for each event of that player, in
// order, after View.Apply has updated the shared player view for that event. A Detector keeps its own
// windows and buffers; it must not retain ev after Handle returns (copy what it needs) and must never
// block.
type Detector interface {
	Handle(c *Ctx, ev *pb.Event)
}

// Factory builds Detectors. Checks lists every check id the detector may emit; each must exist in the
// catalog with Where == Cloud.
type Factory struct {
	Name   string
	Checks []string
	New    func() Detector
}

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register adds a factory. Call it from an init function. It panics on a duplicate name, an empty
// check list or a check id that is not a cloud check in the catalog, so mistakes fail at start-up.
func Register(f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if f.Name == "" || f.New == nil || len(f.Checks) == 0 {
		panic("checks: invalid factory " + f.Name)
	}
	if _, dup := registry[f.Name]; dup {
		panic("checks: duplicate factory " + f.Name)
	}
	for _, id := range f.Checks {
		s, ok := Lookup(id)
		if !ok || s.Where != Cloud {
			panic(fmt.Sprintf("checks: factory %s emits unknown or non-cloud check %q", f.Name, id))
		}
	}
	registry[f.Name] = f
}

// Factories returns the registered factories sorted by name.
func Factories() []Factory {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Factory, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Config is the tenant's check configuration (tenant_checks, via KV PE_TENANTS).
type Config interface {
	// CheckEnabled reports whether a check is enabled; def is the catalog default.
	CheckEnabled(id string, def bool) bool
	// CheckParam returns a threshold override, or def.
	CheckParam(id, name string, def float64) float64
}

// DefaultConfig enables every check that is enabled by default and overrides nothing.
type DefaultConfig struct{}

func (DefaultConfig) CheckEnabled(_ string, def bool) bool        { return def }
func (DefaultConfig) CheckParam(_, _ string, def float64) float64 { return def }

// Emission is one detection result. EventTimeUs defaults to the current event's plugin time.
type Emission struct {
	Check       string
	Points      float64 // raw points from CHECKS.md (severity-scaled where specified)
	Confidence  float64 // 0..1
	Info        string
	Data        map[string]float64
	EventTimeUs uint64
}

// Ctx is what a Detector sees while handling one event.
type Ctx struct {
	View   *View
	Config Config
	// Silent suppresses emissions (worker warm start and replays that only rebuild state).
	Silent bool

	emit  func(Emission)
	event *pb.Event
}

// NewCtx returns a context whose emissions go to emit. The worker reuses one Ctx per player and calls
// SetEvent before each Handle.
func NewCtx(view *View, cfg Config, emit func(Emission)) *Ctx {
	if cfg == nil {
		cfg = DefaultConfig{}
	}
	return &Ctx{View: view, Config: cfg, emit: emit}
}

// SetEvent records the event being handled (used for EventTimeUs defaults).
func (c *Ctx) SetEvent(ev *pb.Event) { c.event = ev }

// Enabled reports whether check id is enabled for this tenant.
func (c *Ctx) Enabled(id string) bool {
	s, ok := Lookup(id)
	if !ok {
		return false
	}
	return c.Config.CheckEnabled(id, s.EnabledDefault)
}

// Param returns the tenant's override for a threshold of check id, or def.
func (c *Ctx) Param(id, name string, def float64) float64 {
	return c.Config.CheckParam(id, name, def)
}

// Emit publishes an emission unless the check is disabled, the context is silent or the player is
// exempt (bedrock / bypass). Points are clamped to >= 0 and confidence to [0, 1]; NaN values drop it.
func (c *Ctx) Emit(e Emission) {
	if c.Silent || c.emit == nil || !c.Enabled(e.Check) || c.View.Exempt || c.View.Bedrock {
		return
	}
	if math.IsNaN(e.Points) || math.IsNaN(e.Confidence) || math.IsInf(e.Points, 0) {
		return
	}
	e.Points = math.Max(0, e.Points)
	e.Confidence = math.Min(1, math.Max(0, e.Confidence))
	if e.EventTimeUs == 0 && c.event != nil {
		e.EventTimeUs = c.event.GetTimeUs()
	}
	c.emit(e)
}
