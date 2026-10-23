package checks_test

import (
	"math"
	"slices"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// strafingTarget makes the target strafe left and right in front of the attacker.
func strafingTarget(w *cfWorld, i int) {
	if (i/8)%2 == 0 {
		w.tgtVel = geom.V(0.22, 0, 0)
	} else {
		w.tgtVel = geom.V(-0.22, 0, 0)
	}
}

func TestHitboxLegitTracking(t *testing.T) {
	for _, protocol := range []int32{769, 47} {
		w := cfNewWorld(t, protocol)
		w.idle(30)
		for i := 0; i < 200; i++ {
			strafingTarget(w, i)
			w.advance()
			aim := w.lookAt(w.targetCentre())
			if i%4 == 0 {
				w.attack()
				w.swing()
			}
			w.move(geom.Vec3{}, aim)
		}
		if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 0 {
			t.Fatalf("protocol %d: tracking a strafing target flagged %d times", protocol, n)
		}
	}
}

// Tick clients pick the target with the rotation of the same tick's movement packet (R1): an attack
// sent while the last rotation (R0) still points away is not a miss.
func TestHitboxUsesR1ForTickClients(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 10; i++ {
		w.advance()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 90}) // looking away
		w.advance()
		w.attack() // R0 = yaw 90
		w.swing()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 0}) // R1 = the target
		w.idle(5)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 0 {
		t.Fatalf("R1 ignored: %d emissions", n)
	}
}

func TestHitboxCheat(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	away := geom.Rotation{Yaw: 90, Pitch: 10} // the target is due south (yaw 0)
	w.move(geom.Vec3{}, away)
	for i := 0; i < 8; i++ {
		w.advance()
		w.attack()
		w.swing()
		w.move(geom.Vec3{}, away)
		w.idle(5)
	}
	es := cfRun(t, "killaura", w)
	if n := checktest.Count(es, checks.KillauraHitbox); n != 6 {
		t.Fatalf("hitbox emissions = %d, want 6 (from the third miss): %+v", n, es)
	}
	if es[0].Points != 5 || es[0].Confidence != 0.85 {
		t.Fatalf("scoring %+v", es[0])
	}
}

// A standing player whose swimming flag is stale (no PONG acknowledged the server's clear) is reported
// with the 0.4 eye but hits from 1.62. Aiming at the target's legs, the same rays from 0.4 pass below
// the box.
func TestHitboxLegitStaleSwimFlag(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	var lastAim geom.Rotation
	for i := 0; i < 200; i++ {
		strafingTarget(w, i)
		w.advance()
		lastAim = w.lookAt(w.tgt.Pos.Add(geom.V(0, 0.5, 0)))
		if i%4 == 0 {
			w.attack(w.lowEye)
			w.swing()
		}
		w.move(geom.Vec3{}, lastAim, pb.MoveFlag_MOVE_FLAG_SWIMMING)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 0 {
		t.Fatalf("standing player with a stale swimming flag flagged %d times", n)
	}
	// The case is meaningful: from the reported eye alone the ray misses the expanded box.
	low := geom.Eye(w.att.Pos, geom.PoseSwimming, 769, 1)
	box := geom.PlayerBox(w.tgt.Pos, 1).Expand(checks.TargetMargin + checks.HitboxTimingMargin + geom.EyeUncertainty(769))
	if _, hit := box.IntersectRay(low, lastAim.Look(), math.Inf(1)); hit {
		t.Fatal("fixture: the ray from the 0.4 eye hits the target")
	}
}

// The standing and sneaking eyes are only added to the reported one: a real swimmer in the water
// hitting the top of a player's head on the bank is judged from 0.4, where the higher eyes' rays pass
// over the box.
func TestHitboxLegitSwimmer(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.att.Pos = geom.V(0, 63, 0)
	w.tgt.Pos = geom.V(0, 64, 2.5)
	w.hist = nil
	low := func() geom.Vec3 { return geom.Eye(w.att.Pos, geom.PoseSwimming, 769, 1) }
	aim, ok := geom.Angles(geom.V(0, 65.75, 2.5).Sub(low()))
	if !ok {
		t.Fatal("aim")
	}
	for range 30 {
		w.advance()
		w.move(geom.Vec3{}, aim, pb.MoveFlag_MOVE_FLAG_SWIMMING)
	}
	for i := 0; i < 20; i++ {
		w.advance()
		w.attack(w.lowEye)
		w.swing()
		w.move(geom.Vec3{}, aim, pb.MoveFlag_MOVE_FLAG_SWIMMING)
		w.advance()
		w.move(geom.Vec3{}, aim, pb.MoveFlag_MOVE_FLAG_SWIMMING)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 0 {
		t.Fatalf("swimmer judged without the 0.4 eye: %d emissions", n)
	}
	box := geom.PlayerBox(w.tgt.Pos, 1).Expand(checks.TargetMargin + checks.HitboxTimingMargin + geom.EyeUncertainty(769))
	for _, pose := range []geom.Pose{geom.PoseStanding, geom.PoseSneaking} {
		if _, hit := box.IntersectRay(geom.Eye(w.att.Pos, pose, 769, 1), aim.Look(), math.Inf(1)); hit {
			t.Fatalf("fixture: the ray from pose %d hits the target", pose)
		}
	}
}

// Misses stay misses with a reduced eye: none of the 0.4, sneaking or standing eyes sees the target.
func TestHitboxCheatWithStaleSwimFlag(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	away := geom.Rotation{Yaw: 90, Pitch: 10}
	w.move(geom.Vec3{}, away, pb.MoveFlag_MOVE_FLAG_SWIMMING)
	for i := 0; i < 8; i++ {
		w.advance()
		w.attack(w.lowEye)
		w.swing()
		w.move(geom.Vec3{}, away, pb.MoveFlag_MOVE_FLAG_SWIMMING)
		w.idle(5)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 6 {
		t.Fatalf("hitbox emissions = %d, want 6", n)
	}
}

// The eye heights an attack is judged from: a plain standing or sneaking report keeps its one eye; the
// reduced eye adds the standing and sneaking ones.
func TestAttackEyeCandidateHeights(t *testing.T) {
	heights := func(pose geom.Pose) []float64 {
		w := cfNewWorld(t, 769)
		var flags []pb.MoveFlag
		mod := func(*pb.Attack) {}
		switch pose {
		case geom.PoseSneaking:
			flags = append(flags, pb.MoveFlag_MOVE_FLAG_SNEAKING)
			mod = func(a *pb.Attack) {
				eye := geom.Eye(w.att.Pos, geom.PoseSneaking, 769, 1)
				a.Sneaking, a.Eye, a.EyeHeight = true, &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, float32(eye.Y-w.att.Pos.Y)
			}
		case geom.PoseSwimming:
			flags, mod = append(flags, pb.MoveFlag_MOVE_FLAG_SWIMMING), w.lowEye
		}
		for range 5 {
			w.advance()
			w.move(geom.Vec3{}, w.att.Rot, flags...)
		}
		w.advance()
		ev := w.attack(mod)
		v := checks.NewView()
		for _, e := range w.events {
			v.Apply(e, 0)
		}
		a, _ := checks.NewAttackObs(v, ev)
		var hs []float64
		for _, e := range a.EyeCandidates() {
			if h := math.Round((e.Y-w.att.Pos.Y)*100) / 100; !slices.Contains(hs, h) {
				hs = append(hs, h)
			}
		}
		slices.Sort(hs)
		return hs
	}
	for name, c := range map[string]struct {
		got, want []float64
	}{
		"standing": {heights(geom.PoseStanding), []float64{1.62}},
		"sneaking": {heights(geom.PoseSneaking), []float64{1.27}},
		"reduced":  {heights(geom.PoseSwimming), []float64{0.4, 1.27, 1.62}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: eye heights %v, want %v", name, c.got, c.want)
		}
	}
}

// Server-forced rotations (teleport, look-at) are not judged.
func TestHitboxServerRotationLegit(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 10; i++ {
		w.advance()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 120}, pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
		w.advance()
		w.attack() // R0 set by the server
		w.swing()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 121}, pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
		w.idle(5)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 0 {
		t.Fatalf("server rotations judged: %d emissions", n)
	}
}

func TestHitboxPendingTimeoutForLegacyClients(t *testing.T) {
	w := cfNewWorld(t, 340) // 1.12: no tick ends, R1 = next move within 100 ms
	w.idle(30)
	away := geom.Rotation{Yaw: -100}
	w.move(geom.Vec3{}, away)
	for i := 0; i < 5; i++ {
		w.advance()
		w.attack()
		w.now += 150_000 // the next move comes too late to be R1
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 0})
		w.move(geom.Vec3{}, away)
		w.idle(5)
	}
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraHitbox); n != 3 {
		t.Fatalf("legacy client: %d emissions, want 3", n)
	}
}

func TestMultiCheat(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 3; i++ {
		w.advance()
		w.attack()
		w.swing()
		w.attack(func(a *pb.Attack) { a.TargetEntityId = 2001; a.TargetPlayer = nil })
		w.swing()
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(5)
	}
	es := cfRun(t, "killaura", w)
	if n := checktest.Count(es, checks.KillauraMulti); n != 3 {
		t.Fatalf("multi emissions = %d, want 3: %+v", n, es)
	}
	if checktest.Points(es, checks.KillauraMulti) != 30 {
		t.Fatal("multi scores 10 points")
	}
}

// Only tick clients are judged: older clients send no tick ends, so "one tick" is unknown.
func TestMultiLegit(t *testing.T) {
	t.Run("legacy client", func(t *testing.T) {
		w := cfNewWorld(t, 340)
		w.idle(30)
		for i := 0; i < 5; i++ {
			w.advance()
			w.attack()
			w.attack(func(a *pb.Attack) { a.TargetEntityId = 2001; a.TargetPlayer = nil })
			w.move(geom.Vec3{}, w.att.Rot)
		}
		if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraMulti); n != 0 {
			t.Fatalf("%d emissions for a non-tick client", n)
		}
	})
	t.Run("consecutive ticks", func(t *testing.T) {
		w := cfNewWorld(t, 769)
		w.idle(30)
		for i := 0; i < 10; i++ {
			w.advance()
			id := int32(2000 + i%2)
			w.attack(func(a *pb.Attack) { a.TargetEntityId = id })
			w.attack(func(a *pb.Attack) { a.TargetEntityId = id }) // two clicks, same target
			w.swing()
			w.swing()
			w.move(geom.Vec3{}, w.att.Rot)
		}
		if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraMulti); n != 0 {
			t.Fatalf("%d emissions for one target per tick", n)
		}
	})
}

func TestNoSwingCheat(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 4; i++ {
		w.advance()
		w.attack()
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(3)
	}
	es := cfRun(t, "killaura", w)
	if n := checktest.Count(es, checks.KillauraNoSwing); n != 2 {
		t.Fatalf("noswing emissions = %d, want 2 (third and fourth attack): %+v", n, es)
	}
	if es[0].Points != 6 || es[0].Confidence != 0.9 {
		t.Fatalf("scoring %+v", es[0])
	}
}

// Another plugin cancelling the swing does not hide it: the plugin reports every client packet, and
// the swing may come in any order within the tick.
func TestNoSwingLegit(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 20; i++ {
		w.advance()
		if i%2 == 0 {
			w.attack()
			w.swing() // cancelled server-side by another plugin, still reported
		} else {
			w.swing()
			w.attack()
		}
		w.move(geom.Vec3{}, w.att.Rot)
		w.idle(2)
	}
	// Spear stabs arrive as PLAYER_DIGGING STAB, not INTERACT_ENTITY: they need no swing here.
	for i := 0; i < 5; i++ {
		w.advance()
		w.attack(func(a *pb.Attack) { a.Stab = true; a.TargetEntityId = 0; a.TargetPlayer = nil })
		w.move(geom.Vec3{}, w.att.Rot)
	}
	// A legacy client's swings cannot be matched to ticks: never judged.
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraNoSwing); n != 0 {
		t.Fatalf("%d noswing emissions", n)
	}
	l := cfNewWorld(t, 340)
	for i := 0; i < 10; i++ {
		l.advance()
		l.attack()
		l.move(geom.Vec3{}, l.att.Rot)
	}
	if n := checktest.Count(cfRun(t, "killaura", l), checks.KillauraNoSwing); n != 0 {
		t.Fatalf("legacy client: %d noswing emissions", n)
	}
}

// The last tick of a session is decided at the quit.
func TestKillauraTickDecidedAtQuit(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	w.advance()
	w.attack()
	w.attack(func(a *pb.Attack) { a.TargetEntityId = 2001 })
	w.quit()
	if n := checktest.Count(cfRun(t, "killaura", w), checks.KillauraMulti); n != 1 {
		t.Fatalf("multi at quit: %d", n)
	}
}
