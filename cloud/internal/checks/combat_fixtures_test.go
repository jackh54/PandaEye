package checks_test

import (
	"math"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// cfWorld builds combat event sequences: an attacker (the player whose events the detectors see)
// and a target whose received positions feed Attack.target_history. Every tick advances both clocks
// by 50 ms. Within a tick the attacker's actions (attacks, swings) come first and its movement packet
// last, as a vanilla client sends them.
type cfWorld struct {
	t      testing.TB
	att    *testkit.Player
	tgt    *testkit.Player
	tick   uint32
	now    uint64
	hist   []*pb.TimedPos
	events []*pb.Event

	tickClient bool
	ping       uint32
	rttHigh    uint32
	tgtVel     geom.Vec3
}

func cfNewWorld(t testing.TB, protocol int32) *cfWorld {
	att := testkit.NewPlayer("attacker")
	att.Protocol = protocol
	tgt := testkit.NewPlayer("target")
	tgt.EntityID = 2000
	tgt.Pos = geom.V(0, 64, 3)
	w := &cfWorld{t: t, att: att, tgt: tgt, now: att.TimeUs, tickClient: protocol >= geom.Protocol1_21_2, ping: 50, rttHigh: 60}
	w.events = append(w.events, att.Join(), att.State())
	// 1.5 s of target history before the first tick.
	for i := 30; i > 0; i-- {
		w.hist = append(w.hist, &pb.TimedPos{TimeUs: w.now - uint64(i)*50_000, X: tgt.Pos.X, Y: tgt.Pos.Y, Z: tgt.Pos.Z})
	}
	w.att.Rot = geom.Rotation{}
	return w
}

func (w *cfWorld) evTick() uint32 {
	if w.tickClient {
		return w.tick
	}
	return 0
}

func (w *cfWorld) base() *pb.Event {
	return &pb.Event{Player: w.att.UUID, TimeUs: w.now, Tick: w.evTick()}
}

// advance starts the next client tick: the target moves by its velocity and its position is received.
func (w *cfWorld) advance() {
	w.tick++
	w.now += 50_000
	w.tgt.Pos = w.tgt.Pos.Add(w.tgtVel)
	w.hist = append(w.hist, &pb.TimedPos{TimeUs: w.now, X: w.tgt.Pos.X, Y: w.tgt.Pos.Y, Z: w.tgt.Pos.Z})
	if len(w.hist) > 64 {
		w.hist = w.hist[len(w.hist)-64:]
	}
}

// history returns the target positions of the last 1500 ms.
func (w *cfWorld) history() []*pb.TimedPos {
	var out []*pb.TimedPos
	for _, p := range w.hist {
		if p.TimeUs+1_500_000 >= w.now && p.TimeUs <= w.now {
			out = append(out, &pb.TimedPos{TimeUs: p.TimeUs, X: p.X, Y: p.Y, Z: p.Z})
		}
	}
	return out
}

// attack adds an attack on the target with the attacker's current rotation (R0).
func (w *cfWorld) attack(mod ...func(*pb.Attack)) *pb.Event {
	eye := w.att.Eye()
	a := &pb.Attack{TargetEntityId: w.tgt.EntityID, TargetPlayer: w.tgt.UUID, TargetType: "minecraft:player",
		Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, Yaw: float32(w.att.Rot.Yaw), Pitch: float32(w.att.Rot.Pitch),
		TargetWidth: geom.PlayerWidth, TargetHeight: geom.PlayerHeight, TargetHistory: w.history(), PingMs: w.ping,
		EntityReach: 3, EyeHeight: float32(eye.Y - w.att.Pos.Y), RttHighMs: w.rttHigh}
	for _, m := range mod {
		m(a)
	}
	e := w.base()
	e.Kind = &pb.Event_Attack{Attack: a}
	w.events = append(w.events, e)
	return e
}

// lowEye makes an attack report the swimming/gliding eye (0.4), as the plugin does while its swimming
// or gliding flag is set. The flag includes values the client has not acknowledged yet, so without
// PONGs it can stay set long after the server cleared it.
func (w *cfWorld) lowEye(a *pb.Attack) {
	eye := geom.Eye(w.att.Pos, geom.PoseSwimming, w.att.Protocol, 1)
	a.Eye, a.EyeHeight = &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, float32(eye.Y-w.att.Pos.Y)
}

func (w *cfWorld) swing() {
	e := w.base()
	e.Kind = &pb.Event_Swing{Swing: &pb.Swing{Hand: pb.Hand_HAND_MAIN}}
	w.events = append(w.events, e)
}

// move sends the attacker's movement packet of the current tick.
func (w *cfWorld) move(d geom.Vec3, r geom.Rotation, flags ...pb.MoveFlag) {
	w.att.Pos = w.att.Pos.Add(d)
	w.att.Rot = r
	var f uint32
	for _, x := range flags {
		f |= uint32(x)
	}
	e := w.base()
	e.Kind = &pb.Event_Move{Move: &pb.MoveSample{X: w.att.Pos.X, Y: w.att.Pos.Y, Z: w.att.Pos.Z, Yaw: float32(r.Yaw),
		Pitch: float32(r.Pitch), OnGround: true, HasPos: true, HasRot: true, Flags: f, TicksSinceLast: 1,
		PacketsSinceLast: 1, Focus: true}}
	w.events = append(w.events, e)
}

// idle advances n ticks with the attacker standing still and keeping its rotation.
func (w *cfWorld) idle(n int) {
	for i := 0; i < n; i++ {
		w.advance()
		w.move(geom.Vec3{}, w.att.Rot)
	}
}

func (w *cfWorld) env(serverTickMs float32) {
	e := w.base()
	e.Kind = &pb.Event_Env{Env: &pb.EnvSample{ServerTickMs: serverTickMs, X: w.att.Pos.X, Y: w.att.Pos.Y, Z: w.att.Pos.Z}}
	w.events = append(w.events, e)
}

func (w *cfWorld) state(mod func(*pb.StateChange)) {
	e := w.att.State()
	e.TimeUs, e.Tick = w.now, w.evTick()
	mod(e.GetState())
	w.events = append(w.events, e)
}

func (w *cfWorld) teleport() {
	e := w.base()
	e.Kind = &pb.Event_Teleport{Teleport: &pb.TeleportApplied{TeleportId: 1, X: w.att.Pos.X, Y: w.att.Pos.Y, Z: w.att.Pos.Z, Confirmed: true}}
	w.events = append(w.events, e)
}

func (w *cfWorld) quit() {
	e := w.base()
	e.Kind = &pb.Event_Quit{Quit: &pb.PlayerQuit{}}
	w.events = append(w.events, e)
}

// lookAt returns the rotation from the attacker's eye to p.
func (w *cfWorld) lookAt(p geom.Vec3) geom.Rotation {
	r, ok := geom.Angles(p.Sub(w.att.Eye()))
	if !ok {
		w.t.Fatal("lookAt: zero direction")
	}
	return r
}

// targetCentre returns the centre of the target's standing box.
func (w *cfWorld) targetCentre() geom.Vec3 { return w.tgt.Pos.Add(geom.V(0, geom.PlayerHeight/2, 0)) }

func cfFactory(t testing.TB, name string) checks.Factory {
	t.Helper()
	for _, f := range checks.Factories() {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("factory %s is not registered", name)
	return checks.Factory{}
}

func cfRun(t testing.TB, name string, w *cfWorld) []checks.Emission {
	t.Helper()
	return checktest.Run(t, cfFactory(t, name), w.events)
}

func cfNear(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
