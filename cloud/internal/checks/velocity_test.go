package checks_test

import (
	"math"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// ---------------------------------------------------------------------------------------------
// rig: event sequences for one player, shared by the velocity, autoclicker, scaffold, dig, inventory
// and latency tests.
// ---------------------------------------------------------------------------------------------

// rig builds the events of one player the way the plugin reports them. Events created between two tick
// calls belong to the current client tick (Event.tick); tick sends that tick's movement packet and ends
// the tick (the tick counter and the event clock advance by one tick / 50 ms).
type rig struct {
	tb     testing.TB
	p      *testkit.Player
	events []*pb.Event
	flags  uint32 // MoveFlag bits of the following movement packets
	ground bool
	hcoll  bool
}

const rigProtocol = 769 // 1.21.4, a tick client

func newRig(tb testing.TB, protocol int32) *rig {
	tb.Helper()
	p := testkit.NewPlayer("rig")
	p.Protocol = protocol
	r := &rig{tb: tb, p: p, ground: true}
	r.add(p.Join(), p.State())
	p.Tick = 1
	if protocol < geom.Protocol1_21_2 {
		p.Tick = 0 // older clients send no tick ends
	}
	return r
}

func (r *rig) add(evs ...*pb.Event) { r.events = append(r.events, evs...) }

func (r *rig) at() *pb.Event { return &pb.Event{Player: r.p.UUID, TimeUs: r.p.TimeUs, Tick: r.p.Tick} }

// wait advances the event clock without ending a client tick.
func (r *rig) wait(d time.Duration) { r.p.Advance(d) }

func (r *rig) tickClient() bool { return r.p.Protocol >= geom.Protocol1_21_2 }

func (r *rig) endTick() {
	if r.tickClient() {
		r.p.Tick++
	}
	r.p.TimeUs += 50_000
}

// tick sends a movement packet with position and rotation and ends the client tick.
func (r *rig) tick(pos geom.Vec3, rot geom.Rotation) *pb.Event {
	e := r.at()
	e.Kind = &pb.Event_Move{Move: &pb.MoveSample{X: pos.X, Y: pos.Y, Z: pos.Z, Yaw: float32(rot.Yaw), Pitch: float32(rot.Pitch),
		OnGround: r.ground, HasPos: true, HasRot: true, Flags: r.flags, TicksSinceLast: 1, PacketsSinceLast: 1,
		Focus: true, HorizontalCollision: r.hcoll}}
	r.p.Pos, r.p.Rot = pos, rot
	r.add(e)
	r.endTick()
	return e
}

// step moves by d keeping the rotation.
func (r *rig) step(d geom.Vec3) *pb.Event { return r.tick(r.p.Pos.Add(d), r.p.Rot) }

// idle ends a client tick without a movement packet (a still tick client).
func (r *rig) idle() { r.endTick() }

func (r *rig) emit(set func(*pb.Event)) *pb.Event {
	e := r.at()
	set(e)
	r.add(e)
	return e
}

func (r *rig) state(edit func(*pb.StateChange)) {
	s := &pb.StateChange{MovementSpeed: 0.1, JumpStrength: 0.42, Gravity: 0.08, StepHeight: 0.6, Scale: 1, EntityReach: 3, BlockReach: 4.5}
	if edit != nil {
		edit(s)
	}
	r.emit(func(e *pb.Event) { e.Kind = &pb.Event_State{State: s} })
}

func (r *rig) velocity(v geom.Vec3) {
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Velocity{Velocity: &pb.VelocityApplied{Source: pb.VelocityApplied_SOURCE_VELOCITY_PACKET,
			Vx: v.X, Vy: v.Y, Vz: v.Z, OnGroundBefore: r.ground}}
	})
}

func (r *rig) explosion(v geom.Vec3) {
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Velocity{Velocity: &pb.VelocityApplied{Source: pb.VelocityApplied_SOURCE_EXPLOSION,
			Vx: v.X, Vy: v.Y, Vz: v.Z, OnGroundBefore: r.ground}}
	})
}

func (r *rig) attack() {
	eye := geom.Eye(r.p.Pos, geom.PoseStanding, r.p.Protocol, 1)
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Attack{Attack: &pb.Attack{TargetEntityId: 77, TargetType: "minecraft:zombie",
			Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, Yaw: float32(r.p.Rot.Yaw), Pitch: float32(r.p.Rot.Pitch),
			PingMs: 50, RttHighMs: 60, EntityReach: 3, EyeHeight: 1.62}}
	})
}

func (r *rig) swing(hand pb.Hand) {
	r.emit(func(e *pb.Event) { e.Kind = &pb.Event_Swing{Swing: &pb.Swing{Hand: hand}} })
}

func (r *rig) useItem() {
	r.emit(func(e *pb.Event) { e.Kind = &pb.Event_UseItem{UseItem: &pb.UseItem{Item: "minecraft:bow"}} })
}

func (r *rig) env(flags pb.EnvFlag) {
	r.emit(func(e *pb.Event) { e.Kind = &pb.Event_Env{Env: &pb.EnvSample{Flags: uint32(flags), ServerTickMs: 50}} })
}

func (r *rig) teleport() {
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Teleport{Teleport: &pb.TeleportApplied{TeleportId: 1, X: r.p.Pos.X, Y: r.p.Pos.Y, Z: r.p.Pos.Z}}
	})
}

func (r *rig) click(window int32, containerOpen bool) {
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Click{Click: &pb.InventoryClick{WindowId: window, Slot: 12, ContainerOpen: containerOpen}}
	})
}

func (r *rig) latency(kind pb.LatencySample_Kind, rttMs float64, unmatched bool) {
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Latency{Latency: &pb.LatencySample{Kind: kind, RttUs: uint32(rttMs * 1000), Unmatched: unmatched}}
	})
}

// stall adds a server-level ServerStall event (empty player, no tick) covering [start, end].
func (r *rig) stall(startUs, endUs uint64) {
	r.add(testkit.Stall(startUs, endUs, 0.5))
}

func (r *rig) run(factory string) []checks.Emission {
	r.tb.Helper()
	return checktest.Run(r.tb, rigFactory(r.tb, factory), r.events)
}

func (r *rig) runWith(factory string, cfg checks.Config) []checks.Emission {
	r.tb.Helper()
	return checktest.RunWith(r.tb, rigFactory(r.tb, factory), r.events, cfg)
}

func rigFactory(tb testing.TB, name string) checks.Factory {
	tb.Helper()
	for _, f := range checks.Factories() {
		if f.Name == name {
			return f
		}
	}
	tb.Fatalf("factory %q is not registered", name)
	return checks.Factory{}
}

// rigConfig is a tenant configuration with overrides.
type rigConfig struct {
	enabled map[string]bool
	params  map[string]float64 // "check/param"
}

func (c rigConfig) CheckEnabled(id string, def bool) bool {
	if v, ok := c.enabled[id]; ok {
		return v
	}
	return def
}

func (c rigConfig) CheckParam(id, name string, def float64) float64 {
	if v, ok := c.params[id+"/"+name]; ok {
		return v
	}
	return def
}

// expectCount fails unless exactly want emissions of check are in es.
func expectCount(tb testing.TB, es []checks.Emission, check string, want int) {
	tb.Helper()
	if got := checktest.Count(es, check); got != want {
		tb.Fatalf("%s: %d emissions, want %d (all: %+v)", check, got, want, es)
	}
}

// expectAtLeast fails unless at least want emissions of check are in es.
func expectAtLeast(tb testing.TB, es []checks.Emission, check string, want int) {
	tb.Helper()
	if got := checktest.Count(es, check); got < want {
		tb.Fatalf("%s: %d emissions, want >= %d (all: %+v)", check, got, want, es)
	}
}

// expectScoring checks the points and confidence of every emission of check.
func expectScoring(tb testing.TB, es []checks.Emission, check string, minPoints, maxPoints, conf float64) {
	tb.Helper()
	spec, ok := checks.Lookup(check)
	if !ok || spec.Confidence != conf {
		tb.Fatalf("%s: catalog confidence %v, test expects %v", check, spec.Confidence, conf)
	}
	for _, e := range es {
		if e.Check != check {
			continue
		}
		if e.Points < minPoints-1e-9 || e.Points > maxPoints+1e-9 || e.Confidence != conf || e.EventTimeUs == 0 || e.Info == "" {
			tb.Fatalf("%s: bad emission %+v (points %v..%v, confidence %v)", check, e, minPoints, maxPoints, conf)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// victim: vanilla knockback physics for the velocity tests.
// ---------------------------------------------------------------------------------------------

type victim struct {
	r       *rig
	mot     geom.Vec3
	groundY float64
	ceilY   float64 // a ceiling at this height (0 = none)
	rot     geom.Rotation
}

func newVictim(r *rig) *victim {
	return &victim{r: r, groundY: r.p.Pos.Y, rot: geom.Rotation{Yaw: 0, Pitch: 0}}
}

// tickOpts is one client tick of the victim.
type tickOpts struct {
	apply *geom.Vec3 // velocity applied by the client this tick (scaled by a cheat)
	add   *geom.Vec3 // explosion knockback added to the motion this tick
	jump  bool
	accel geom.Vec3 // horizontal input acceleration this tick
	boost geom.Vec3 // sprint-jump boost (only with jump)
}

// step runs one vanilla client tick (velocity, jump, input, move with ground/ceiling collision, then
// gravity and friction with the pre-move ground state) and sends the movement packet.
func (vi *victim) step(o tickOpts) {
	r := vi.r
	if o.apply != nil {
		vi.mot = *o.apply
	}
	if o.add != nil {
		vi.mot = vi.mot.Add(*o.add)
	}
	for _, c := range []*float64{&vi.mot.X, &vi.mot.Y, &vi.mot.Z} {
		if math.Abs(*c) < 0.003 {
			*c = 0
		}
	}
	wasGround := r.ground
	if o.jump && wasGround {
		vi.mot.Y = math.Max(0.42, vi.mot.Y)
		vi.mot = vi.mot.Add(o.boost)
	}
	vi.mot = vi.mot.Add(o.accel)
	pos := r.p.Pos.Add(vi.mot)
	r.ground = false
	if pos.Y <= vi.groundY {
		pos.Y, vi.mot.Y, r.ground = vi.groundY, 0, true
	}
	if vi.ceilY != 0 && pos.Y+1.8 > vi.ceilY {
		pos.Y, vi.mot.Y = vi.ceilY-1.8, 0
	}
	r.tick(pos, vi.rot)
	f := 0.91
	if wasGround {
		f = 0.6 * 0.91
	}
	vi.mot.X *= f
	vi.mot.Z *= f
	vi.mot.Y = (vi.mot.Y - 0.08) * 0.98
}

// knockback sends a velocity, lets the client apply it two ticks later with response (nil = vanilla),
// then runs physics for 18 more ticks with the given per-tick options.
func (vi *victim) knockback(v geom.Vec3, apply func(geom.Vec3) *geom.Vec3, first tickOpts, rest tickOpts) {
	vi.r.velocity(v)
	vi.step(tickOpts{accel: rest.accel})
	vi.step(tickOpts{accel: rest.accel})
	a := &v
	if apply != nil {
		a = apply(v)
	}
	first.apply = a
	vi.step(first)
	for range 18 {
		vi.step(rest)
	}
}

func ignoreAll(geom.Vec3) *geom.Vec3 { return nil }

func TestVelocityLegitStandingVictim(t *testing.T) {
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 12 {
		vi.knockback(geom.V(0, 0.4, -0.4), nil, tickOpts{}, tickOpts{})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityLegitJumpReset(t *testing.T) {
	// The victim jumps in the tick the knockback applies: dy = max(vy, jump power), and sprint-jumps into
	// the attacker (boost and input against the knockback).
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 12 {
		vi.knockback(geom.V(0, 0.36, -0.4), nil,
			tickOpts{jump: true, boost: geom.V(0, 0, 0.2), accel: geom.V(0, 0, 0.13)},
			tickOpts{accel: geom.V(0, 0, 0.026)})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityLegitSpeedIIIntoAttacker(t *testing.T) {
	// Speed II (movement_speed 0.14) sprinting and sprint-jumping into the attacker: the input cancels a
	// large part of the knockback.
	r := newRig(t, rigProtocol)
	r.state(func(s *pb.StateChange) { s.MovementSpeed = 0.14 })
	vi := newVictim(r)
	for i := range 12 {
		first := tickOpts{accel: geom.V(0, 0, 0.14*1.3)}
		if i%2 == 0 {
			first = tickOpts{jump: true, boost: geom.V(0, 0, 0.2), accel: geom.V(0, 0, 0.14*1.3)}
		}
		vi.knockback(geom.V(0.1, 0.4, -0.44), nil, first, tickOpts{accel: geom.V(0, 0, 0.026)})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)

	// A much faster player (movement_speed 0.2, e.g. Speed V) sprint-jumping into a 0.6 knockback keeps
	// only 0.14 of it: legit with the attribute, a shortfall of 0.21 with the default speed.
	fast := newRig(t, rigProtocol)
	fast.state(func(s *pb.StateChange) { s.MovementSpeed = 0.2 })
	fvi := newVictim(fast)
	for range 12 {
		fvi.knockback(geom.V(0, 0.4, -0.6), nil, tickOpts{jump: true, boost: geom.V(0, 0, 0.2), accel: geom.V(0, 0, 0.26)},
			tickOpts{accel: geom.V(0, 0, 0.026)})
	}
	es = fast.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityLegitExplosionWhileFalling(t *testing.T) {
	// TNT knockback while falling: the explosion adds to the motion (and the damage knockback packet
	// follows): nothing is judged around explosions.
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	vi.groundY = 40
	for range 8 {
		r.ground = false
		vi.mot = geom.V(0, -0.6, 0)
		vi.step(tickOpts{})
		r.explosion(geom.V(0.3, 0.5, 0.2))
		r.velocity(geom.V(0.2, 0.45, 0.1))
		vi.step(tickOpts{})
		add := geom.V(0.3, 0.5, 0.2)
		vi.step(tickOpts{add: &add})
		for range 14 {
			vi.step(tickOpts{})
		}
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityLegitLowCeiling(t *testing.T) {
	// A 2-block-high tunnel: the head hits the ceiling after 0.2 blocks.
	r := newRig(t, rigProtocol)
	r.env(pb.EnvFlag_ENV_FLAG_LOW_CEILING | pb.EnvFlag_ENV_FLAG_NEAR_SOLID)
	vi := newVictim(r)
	vi.ceilY = r.p.Pos.Y + 2
	for range 12 {
		vi.knockback(geom.V(0, 0.4, -0.4), nil, tickOpts{}, tickOpts{})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityLegitHorizontalCollision(t *testing.T) {
	// Knocked into a wall: the client reports the collision and the horizontal check skips the move.
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 12 {
		vi.r.velocity(geom.V(0, 0.4, -0.5))
		vi.step(tickOpts{})
		vi.step(tickOpts{})
		r.hcoll = true
		v := geom.V(0, 0.4, 0)
		vi.step(tickOpts{apply: &v})
		r.hcoll = false
		for range 18 {
			vi.step(tickOpts{})
		}
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectCount(t, es, "velocity.horizontal", 0)
}

func TestVelocityCheatIgnoresKnockback(t *testing.T) {
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 12 {
		vi.knockback(geom.V(0, 0.4, -0.4), ignoreAll, tickOpts{}, tickOpts{})
	}
	es := r.run("velocity")
	// 12 ignored: a violation after 4, the window restarts, again after 8 and 12.
	expectCount(t, es, "velocity.vertical", 3)
	expectCount(t, es, "velocity.horizontal", 0) // never matched, so never judged horizontally
	expectScoring(t, es, "velocity.vertical", 6, 6, 0.85)
}

func TestVelocityCheatReducedHorizontal(t *testing.T) {
	// Full vertical, 60% horizontal knockback while airborne (jump-hitting).
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	reduce := func(v geom.Vec3) *geom.Vec3 { w := geom.V(v.X*0.6, v.Y, v.Z*0.6); return &w }
	for range 12 {
		r.ground = false
		vi.mot = geom.V(0, 0.2, 0)
		vi.knockback(geom.V(0, 0.36, -0.5), reduce, tickOpts{}, tickOpts{})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
	expectAtLeast(t, es, "velocity.horizontal", 2)
	expectScoring(t, es, "velocity.horizontal", 5, 5, 0.8)
}

func TestVelocityNotJudged(t *testing.T) {
	cases := map[string]func(r *rig, vi *victim){
		"water": func(r *rig, vi *victim) { r.env(pb.EnvFlag_ENV_FLAG_IN_WATER) },
		"web":   func(r *rig, vi *victim) { r.env(pb.EnvFlag_ENV_FLAG_STUCK | pb.EnvFlag_ENV_FLAG_IN_WEB) },
		"gliding": func(r *rig, vi *victim) {
			r.flags = uint32(pb.MoveFlag_MOVE_FLAG_GLIDING)
			r.step(geom.V(0, 0, 0))
		},
		"creative": func(r *rig, vi *victim) {
			r.state(func(s *pb.StateChange) { s.GameMode = pb.GameMode_GAME_MODE_CREATIVE })
		},
		"flying":    func(r *rig, vi *victim) { r.state(func(s *pb.StateChange) { s.Flying, s.AllowFlight = true, true }) },
		"small vy":  nil,
		"teleports": nil,
		"legacy":    nil,
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			protocol := int32(rigProtocol)
			if name == "legacy" {
				protocol = 340 // 1.12.2 via ViaVersion: no tick numbers, still judged by packet order
			}
			r := newRig(t, protocol)
			vi := newVictim(r)
			if setup != nil {
				setup(r, vi)
			}
			for range 12 {
				v := geom.V(0, 0.4, -0.4)
				if name == "small vy" {
					v.Y = 0.1
				}
				r.velocity(v)
				if name == "teleports" {
					r.teleport()
				}
				for range 20 {
					vi.step(tickOpts{})
				}
			}
			es := r.run("velocity")
			want := 0
			if name == "legacy" {
				want = 3 // legacy clients are judged too (packet order instead of ticks)
			}
			expectCount(t, es, "velocity.vertical", want)
			expectCount(t, es, "velocity.horizontal", 0)
		})
	}
}

func TestVelocityNewerReplacesOlder(t *testing.T) {
	// A second knockback before the first applied: only the newer one is judged, and it matches.
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 12 {
		r.velocity(geom.V(0, 0.3, -0.3))
		vi.step(tickOpts{})
		vi.knockback(geom.V(0, 0.4, -0.4), nil, tickOpts{}, tickOpts{})
	}
	es := r.run("velocity")
	expectCount(t, es, "velocity.vertical", 0)
}

func TestVelocityTenantThreshold(t *testing.T) {
	r := newRig(t, rigProtocol)
	vi := newVictim(r)
	for range 5 {
		vi.knockback(geom.V(0, 0.4, -0.4), ignoreAll, tickOpts{}, tickOpts{})
	}
	cfg := rigConfig{params: map[string]float64{"velocity.vertical/need": 6}}
	expectCount(t, r.runWith("velocity", cfg), "velocity.vertical", 0)
	expectCount(t, r.run("velocity"), "velocity.vertical", 1)
	off := rigConfig{enabled: map[string]bool{"velocity.vertical": false}}
	expectCount(t, r.runWith("velocity", off), "velocity.vertical", 0)
}

func TestVelocityRules(t *testing.T) {
	// Direct checks of the CHECKS.md 2.10 shortfall formula through a single matched knockback each.
	for _, c := range []struct {
		name   string
		ground bool
		h      float64
		want   bool
	}{
		{"air full", false, 0.5, false},
		{"air 60%", false, 0.3, true},
		{"ground within input", true, 0.17, false},
		{"ground zero", true, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigProtocol)
			for range 6 {
				r.ground = c.ground
				r.step(geom.V(0, 0, 0))
				r.velocity(geom.V(0, 0.4, -0.5))
				r.ground = false
				r.step(geom.V(0, 0.4, -c.h))
				for range 12 {
					r.step(geom.V(0, -0.05, 0))
				}
				r.ground = true
				r.step(geom.V(0, 0, 0))
				r.wait(time.Second)
			}
			es := r.run("velocity")
			expectCount(t, es, "velocity.vertical", 0)
			got := checktest.Count(es, "velocity.horizontal") > 0
			if got != c.want {
				t.Fatalf("horizontal emitted = %v, want %v (%+v)", got, c.want, es)
			}
		})
	}
}
