package checks_test

import (
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
