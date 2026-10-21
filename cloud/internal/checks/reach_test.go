package checks_test

import (
	"math"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// retreatHistory returns 1.5 s of target positions (every 50 ms up to T) of a target sprinting away
// along +z at speed blocks per tick whose box was at boxDist from the attacker's eye (at the origin
// column) at time seenUs.
func retreatHistory(T, seenUs uint64, boxDist, speed float64) []*pb.TimedPos {
	var out []*pb.TimedPos
	for k := 30; k >= 0; k-- {
		tt := T - uint64(k)*50_000
		z := boxDist + geom.PlayerWidth/2 + speed*float64(int64(tt)-int64(seenUs))/50_000
		out = append(out, &pb.TimedPos{TimeUs: tt, X: 0, Y: 64, Z: z})
	}
	return out
}

// The review's legit case: a target retreating at sprint speed, last seen by the attacker at 2.95
// blocks, 150 ms ping, server at 15 TPS. When the attack arrives the target is far out of reach.
func TestReachRetreatingTargetLegit(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.ping, w.rttHigh = 150, 160
	w.env(66.7)
	var last *pb.Event
	for i := 0; i < 12; i++ {
		w.idle(10)
		w.advance()
		T := w.now
		// Server tracker interval (2 ticks at 15 TPS), one-way latency, client interpolation and the
		// way back: the attacker saw the target about 430 ms before the attack arrived.
		hist := retreatHistory(T, T-430_000, 2.95, 0.28)
		last = w.attack(func(a *pb.Attack) { a.TargetHistory = hist })
		w.swing()
		w.move(geom.Vec3{}, w.att.Rot)
	}
	es := cfRun(t, "reach", w)
	if n := checktest.Count(es, checks.ReachCheck); n != 0 {
		t.Fatalf("retreating target flagged %d times: %+v", n, es)
	}
	// The case is meaningful: the newest target position is well out of reach.
	h := last.GetAttack().GetTargetHistory()
	newest := h[len(h)-1]
	if d := newest.Z - 0.3; d < 4.5 {
		t.Fatalf("fixture: newest target box distance %.2f is not out of reach", d)
	}
}

// Jumping while hitting at exactly the vanilla limit: at every phase of the jump the target box is
// exactly 3.0 blocks from the attacker's eye (the farthest a vanilla client can pick it).
func TestReachJumpHitAtExactly3Legit(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	jump := []float64{0.42, 0.3332, 0.2486, 0.1664, 0.0865, 0.0089, -0.0663, -0.1394, -0.2104, -0.2795, -0.3460, -0.3600}
	for round := 0; round < 4; round++ {
		for _, dy := range jump {
			w.advance()
			eye := w.att.Eye()
			above := math.Max(0, eye.Y-(64+geom.PlayerHeight))
			z := math.Sqrt(9-above*above) + geom.PlayerWidth/2
			var hist []*pb.TimedPos
			for k := 20; k >= 0; k-- {
				hist = append(hist, &pb.TimedPos{TimeUs: w.now - uint64(k)*50_000, X: 0, Y: 64, Z: z})
			}
			w.attack(func(a *pb.Attack) { a.Sprinting = true; a.TargetHistory = hist })
			w.swing()
			w.move(geom.V(0, dy, 0), w.lookAt(geom.V(0, 64.9, z)))
		}
		w.att.Pos.Y = 64 // land exactly
		w.idle(10)
	}
	es := cfRun(t, "reach", w)
	if n := checktest.Count(es, checks.ReachCheck); n != 0 {
		t.Fatalf("jump hits at 3.0 flagged %d times: %+v", n, es)
	}
}

func TestReachCheat(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.tgt.Pos = geom.V(0, 64, 3.9) // box face at 3.6
	w.hist = nil
	w.idle(30)
	for i := 0; i < 5; i++ {
		w.advance()
		w.attack()
		w.swing()
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(10)
	}
	es := cfRun(t, "reach", w)
	if n := checktest.Count(es, checks.ReachCheck); n != 4 {
		t.Fatalf("reach emissions = %d, want 4 (every attack from the second): %+v", n, es)
	}
	excess := (3.6 - checks.TargetMargin) - (3 + geom.EyeUncertainty(769))
	e := es[0]
	if !cfNear(e.Points, 3+10*excess/1.5) || !cfNear(e.Confidence, 0.7+0.25*math.Min(excess/0.5, 1)) || !cfNear(e.Data["excess"], excess) {
		t.Fatalf("scoring %+v, excess %v", e, excess)
	}
}

func TestReachThresholdOverride(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.tgt.Pos = geom.V(0, 64, 3.9)
	w.hist = nil
	w.idle(30)
	for i := 0; i < 5; i++ {
		w.advance()
		w.attack()
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(10)
	}
	cfg := overrideConfig{params: map[string]float64{checks.ReachCheck + "/min_excess": 1.0}}
	if n := checktest.Count(checktest.RunWith(t, cfFactory(t, "reach"), w.events, cfg), checks.ReachCheck); n != 0 {
		t.Fatalf("tenant override ignored: %d emissions", n)
	}
	cfg = overrideConfig{disabled: map[string]bool{checks.ReachCheck: true}}
	if n := checktest.Count(checktest.RunWith(t, cfFactory(t, "reach"), w.events, cfg), checks.ReachCheck); n != 0 {
		t.Fatalf("disabled check emitted %d times", n)
	}
}

// Attacks that CHECKS.md 2.1 skips never count, however far the target is.
func TestReachSkips(t *testing.T) {
	cases := map[string]func(w *cfWorld){
		"extended reach": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.ExtendedReach = true })
		},
		"spear stab": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.Stab = true; a.TargetEntityId = 0; a.TargetPlayer = nil })
		},
		"creative": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.GameMode = pb.GameMode_GAME_MODE_CREATIVE })
		},
		"vehicle": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.AttackerInVehicle = true })
		},
		"target in vehicle": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.TargetInVehicle = true })
		},
		"ping above 600 ms": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.PingMs, a.RttHighMs = 700, 720 })
		},
		"after a teleport": func(w *cfWorld) {
			w.teleport()
			w.attack()
		},
		"not a player": func(w *cfWorld) {
			w.attack(func(a *pb.Attack) { a.TargetPlayer = nil; a.TargetType = "minecraft:zombie" })
		},
		"server stall": func(w *cfWorld) {
			e := w.base()
			e.Kind = &pb.Event_Stall{Stall: &pb.ServerStall{StartUs: w.now - 200_000, EndUs: w.now - 100_000, StalledShare: 0.5}}
			w.events = append(w.events, e)
			w.attack()
		},
	}
	for name, attack := range cases {
		t.Run(name, func(t *testing.T) {
			w := cfNewWorld(t, 769)
			w.tgt.Pos = geom.V(0, 64, 5)
			w.hist = nil
			w.idle(30)
			for i := 0; i < 5; i++ {
				w.advance()
				attack(w)
				w.move(geom.Vec3{}, w.att.Rot)
				w.idle(25)
			}
			if n := checktest.Count(cfRun(t, "reach", w), checks.ReachCheck); n != 0 {
				t.Fatalf("%s: %d emissions", name, n)
			}
		})
	}
}

// Legacy clients (1.8, ViaVersion-translated) get reach with the legacy eye uncertainty.
func TestReachLegacyClient(t *testing.T) {
	w := cfNewWorld(t, 47)
	w.tgt.Pos = geom.V(0, 64, 3.9)
	w.hist = nil
	w.idle(30)
	for i := 0; i < 3; i++ {
		w.advance()
		w.attack()
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(10)
	}
	es := cfRun(t, "reach", w)
	if n := checktest.Count(es, checks.ReachCheck); n != 2 || !cfNear(es[0].Data["limit"], 3.03) {
		t.Fatalf("legacy reach: %+v", es)
	}
}

type overrideConfig struct {
	params   map[string]float64
	disabled map[string]bool
}

func (c overrideConfig) CheckEnabled(id string, def bool) bool { return def && !c.disabled[id] }
func (c overrideConfig) CheckParam(id, name string, def float64) float64 {
	if v, ok := c.params[id+"/"+name]; ok {
		return v
	}
	return def
}
