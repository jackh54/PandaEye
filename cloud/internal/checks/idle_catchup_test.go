package checks_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Out of focus (no attack, velocity, placement, dig or window click for 3 to 5 s) the plugin sends
// 2 MoveSamples per second; focus, and full-rate movement, starts with the action itself. Plugins before
// the catch-up therefore reported the first action after a pause without the positions just before it:
// the cloud's last position was up to 500 ms old, so the eye on the path from the previous position
// (CHECKS 2.1) was not a candidate, and a moving player's legit click was judged from the reported eye,
// up to a tick of movement ahead of the one the client picked from. Since the catch-up the plugin sends
// the (up to 4) moves it held back right before the action. The cloud is unchanged: these tests feed it
// both streams.

// idleWalk walks ticks ticks by step looking at rot, as the cloud receives it out of focus: a MoveSample
// every 10th tick (2 Hz) and, with caughtUp, the last 4 movement packets that the plugin sends before the
// action.
func idleWalk(r *rig, ticks int, step geom.Vec3, rot geom.Rotation, caughtUp bool) {
	for i := range ticks {
		pos := r.p.Pos.Add(step)
		if i%10 == 0 || caughtUp && i >= ticks-4 {
			r.tick(pos, rot)
		} else {
			r.p.Pos = pos // the client sent it; the plugin held it back
			r.idle()
		}
	}
}

// pauseThenPlace is one placement per pause: the player walks east for 3.5 s out of focus, looking north
// and down, and places on the top face of a floor block. The client picks from between the previous and
// the last position (partial tick 0.1: a tick of movement behind the reported eye). target returns the
// block and the point the player looks at, from the eye the client picked from; when cheat is set the
// placement goes to the block behind the player instead, with the same rotation.
func pauseThenPlace(r *rig, episodes int, speed float64, caughtUp, cheat bool) {
	step := geom.V(speed, 0, 0)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{Pitch: 50})
	for range episodes {
		rot := r.p.Rot
		idleWalk(r, 70, step, rot, caughtUp)
		cur := r.p.Pos
		prev := cur.Sub(step)
		pick := geom.Eye(prev.Add(step.Scale(0.1)), geom.PoseStanding, r.p.Protocol, 1)
		b := [3]int32{int32(math.Floor(pick.X)), 63, int32(math.Floor(pick.Z)) - 2}
		target := geom.V(float64(b[0])+0.5, 64, float64(b[2])+0.5)
		rot = aim(pick, target)
		face, cursor, ok := rayClick(pick, rot, b)
		if !ok {
			panic("fixture: the look ray misses the block")
		}
		if cheat {
			b[2] += 4 // behind the player, out of view
			face, cursor = pb.Face_FACE_UP, geom.V(0.5, 1, 0.5)
		}
		// The player turned to rot in an earlier, held-back tick: R0 = rot, no turn in the click tick.
		r.p.Rot = rot
		r.place(blockClick{block: b, face: face, cursor: cursor, eye: geom.Eye(cur, geom.PoseStanding, r.p.Protocol, 1), rot: rot})
		r.tick(cur.Add(step), rot) // the same tick's movement packet (R1), in focus from here
		for range 3 {
			r.step(step)
		}
		r.tick(r.p.Pos, rot) // stops
	}
}

func TestScaffoldLegitFirstPlacementAfterAPauseWhileWalking(t *testing.T) {
	const walking = 0.216
	// With the moves before the placement (plugin catch-up): the picked eye is on the eye path.
	r := newRig(t, rigProtocol)
	pauseThenPlace(r, 12, walking, true, false)
	es, d := r.runDigest("scaffold")
	if len(es) != 0 {
		t.Fatalf("legit first placements after a pause: %+v", es)
	}
	if n := len(d.of("scaffold.cursor")); n != 12 {
		t.Fatalf("%d placements judged, want 12", n)
	}
	for _, o := range d.of("scaffold.cursor") {
		if o.Failed || !o.EyePath || o.GapUs >= 0 && o.GapUs < 3_000_000 {
			t.Fatalf("each placement is the first after 3 s and judged with its eye path: %+v", o)
		}
	}
	// Without them (plugins before the catch-up) the cloud cannot find that eye: the click position is
	// about 5 degrees off every candidate look ray, and every such placement mismatches.
	old := newRig(t, rigProtocol)
	pauseThenPlace(old, 12, walking, false, false)
	es, d = old.runDigest("scaffold")
	expectCount(t, es, "scaffold.cursor", 4)
	expectCount(t, es, "scaffold.ray", 0)
	if n := len(d.of("scaffold.cursor")); n != 12 {
		t.Fatalf("%d placements judged, want 12", n)
	}
	for _, o := range d.of("scaffold.cursor") {
		if !o.Failed || o.EyePath || !o.RayHit || o.Value < 3 || o.Value > 8 {
			t.Fatalf("the old stream's mismatch: %+v", o)
		}
	}
}

func TestScaffoldCheatOutOfViewAfterAPause(t *testing.T) {
	// The eye path restores the eyes of the last tick of movement, nothing more: a block behind the
	// player is still out of view, walking or standing.
	for _, speed := range []float64{0.216, 0} {
		r := newRig(t, rigProtocol)
		pauseThenPlace(r, 12, speed, true, true)
		es, d := r.runDigest("scaffold")
		expectCount(t, es, "scaffold.ray", 4) // a violation every 3 misses
		expectCount(t, es, "scaffold.cursor", 4)
		for _, o := range d.of("scaffold.ray") {
			if !o.Failed || o.Value < 45 {
				t.Fatalf("speed %v: a placement behind the player must miss by a large angle: %+v", speed, o)
			}
		}
	}
}

// After a pause, a dig START at a block out of view is still a miss with the caught-up moves.
func TestDigCheatOutOfViewAfterAPause(t *testing.T) {
	r := newRig(t, rigProtocol)
	step := geom.V(0.216, 0, 0)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{Yaw: 180, Pitch: 50}) // north (-z) and down
	for range 12 {
		idleWalk(r, 70, step, r.p.Rot, true)
		cur := r.p.Pos
		b := [3]int32{int32(math.Floor(cur.X)), 64, int32(math.Floor(cur.Z)) + 3} // behind, looking north
		c := blockClick{block: b, face: pb.Face_FACE_NORTH, eye: geom.Eye(cur, geom.PoseStanding, r.p.Protocol, 1), rot: r.p.Rot}
		r.dig(pb.BlockDig_ACTION_START, c)
		r.tick(cur.Add(step), r.p.Rot)
		r.tick(r.p.Pos, r.p.Rot)
	}
	es, d := r.runDigest("dig")
	expectCount(t, es, "dig.ray", 3) // >= 4 of the last 10
	for _, o := range d.of("dig.ray") {
		if !o.Failed || !o.EyePath {
			t.Fatalf("an out-of-view dig after a pause, judged with its eye path, must miss: %+v", o)
		}
	}
}

// randomFirstPlacements are vanilla-consistent placements, each the first after a 3.5 s pause, sneaking,
// walking or sprinting in any direction and picked from anywhere between the previous and the last
// position, as the cloud receives them with or without the caught-up moves. It returns the per-check
// failure rates the digest would count.
func randomFirstPlacements(t *testing.T, seed uint64, speed float64, caughtUp bool) map[string]float64 {
	rng := rand.New(rand.NewPCG(seed, 41))
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	const n = 300
	for i := 0; i < n; {
		step := geom.LookVector(rng.Float64()*360-180, 0).Scale(speed)
		idleWalk(r, 70, step, r.p.Rot, caughtUp)
		cur := r.p.Pos
		pick := geom.Eye(cur.Sub(step).Add(step.Scale(rng.Float64())), geom.PoseStanding, r.p.Protocol, 1)
		rot := geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: 20 + rng.Float64()*70}
		p := pick.Add(rot.Look().Scale(1 + rng.Float64()*3))
		b := [3]int32{int32(math.Floor(p.X)), int32(math.Floor(p.Y)), int32(math.Floor(p.Z))}
		face, cursor, ok := rayClick(pick, rot, b)
		if !ok {
			continue
		}
		r.p.Rot = rot
		r.place(blockClick{block: b, face: face, cursor: cursor, eye: geom.Eye(cur, geom.PoseStanding, r.p.Protocol, 1), rot: rot})
		r.tick(cur.Add(step), rot)
		r.tick(r.p.Pos, rot)
		i++
	}
	_, d := r.runDigest("scaffold")
	rates := map[string]float64{}
	for _, o := range d.obs {
		if o.Failed {
			rates[o.Check] += 1.0 / n
		}
	}
	return rates
}

// idleWalkPhase is idleWalk with the 2 Hz samples placed by phase: the last sample is the packet phase
// packets before the click's tick (phase 0: the packet of the tick before the click). With caughtUp, the
// packets after that sample, at most 4, are sent before the action as the plugin's catch-up does.
func idleWalkPhase(r *rig, ticks int, step geom.Vec3, rot geom.Rotation, phase int, caughtUp bool) {
	for i := range ticks {
		pos := r.p.Pos.Add(step)
		back := ticks - 1 - i // 0 = the packet of the tick before the click
		if back%10 == phase || caughtUp && back < phase && back < 4 {
			r.tick(pos, rot)
		} else {
			r.p.Pos = pos
			r.idle()
		}
	}
}

// phaseFirstPlacements is randomFirstPlacements at one sample phase; it returns the scaffold.cursor
// failure rate and the share of the placements judged with an eye path.
func phaseFirstPlacements(t *testing.T, seed uint64, speed float64, phase int, caughtUp bool) (cursor, eyePath float64) {
	rng := rand.New(rand.NewPCG(seed, 43))
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	const n = 200
	for i := 0; i < n; {
		step := geom.LookVector(rng.Float64()*360-180, 0).Scale(speed)
		idleWalkPhase(r, 70, step, r.p.Rot, phase, caughtUp)
		cur := r.p.Pos
		pick := geom.Eye(cur.Sub(step).Add(step.Scale(rng.Float64())), geom.PoseStanding, r.p.Protocol, 1)
		rot := geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: 20 + rng.Float64()*70}
		p := pick.Add(rot.Look().Scale(1 + rng.Float64()*3))
		b := [3]int32{int32(math.Floor(p.X)), int32(math.Floor(p.Y)), int32(math.Floor(p.Z))}
		face, c, ok := rayClick(pick, rot, b)
		if !ok {
			continue
		}
		r.p.Rot = rot
		r.place(blockClick{block: b, face: face, cursor: c, eye: geom.Eye(cur, geom.PoseStanding, r.p.Protocol, 1), rot: rot})
		r.tick(cur.Add(step), rot)
		r.tick(r.p.Pos, rot)
		i++
	}
	_, d := r.runDigest("scaffold")
	cur, ray := d.of("scaffold.cursor"), d.of("scaffold.ray")
	if len(ray) != n || len(cur) < n*9/10 {
		t.Fatalf("%d placements judged by scaffold.ray, %d by scaffold.cursor, want %d", len(ray), len(cur), n)
	}
	failed, withPath := 0, 0
	for _, o := range cur {
		if o.Failed {
			failed++
		}
	}
	for _, o := range ray {
		if o.EyePath {
			withPath++
		}
	}
	return float64(failed) / float64(len(cur)), float64(withPath) / float64(len(ray))
}

// The 2 Hz sample falls on any packet; the catch-up restores the eye path whenever a packet was held back
// after it. When the sample is the packet of the tick before the click (1 in 10 at 2 Hz) nothing is held,
// the catch-up sends nothing, and the cloud's previous position is the sample before, 10 ticks earlier: a
// coarse eye path over those ticks, with which a walking player's legit first placement still mismatches
// often. The plugin cannot send the packet before that sample afterwards (the cloud would drop it as
// stale), so this case stays as it was.
func TestScaffoldFirstPlacementBySamplePhase(t *testing.T) {
	const walking = 0.216
	for phase := range 10 {
		cursor, eyePath := phaseFirstPlacements(t, uint64(phase), walking, phase, true)
		oldCursor, _ := phaseFirstPlacements(t, uint64(phase), walking, phase, false)
		t.Logf("phase %d: caught up %.1f%% mismatched (eye path %.0f%%), without %.1f%%", phase, 100*cursor, 100*eyePath, 100*oldCursor)
		switch {
		case phase > 0 && (cursor != 0 || eyePath != 1):
			t.Fatalf("phase %d: with a held packet every legit first placement has its eye path and matches: %.3f, eye path %.3f", phase, cursor, eyePath)
		case phase > 0 && oldCursor < 0.15:
			t.Fatalf("phase %d: the fixture should reproduce the old stream's mismatches: %.3f", phase, oldCursor)
		case phase == 0 && (cursor != oldCursor || cursor < 0.1):
			t.Fatalf("phase 0: nothing is held, the stream and its mismatches are the old ones: %.3f vs %.3f", cursor, oldCursor)
		}
	}
}

func TestScaffoldPropertyRandomFirstPlacementAfterAPause(t *testing.T) {
	for i, speed := range []float64{0.065, 0.216, 0.28} { // sneaking, walking, sprinting
		if rates := randomFirstPlacements(t, uint64(i), speed, true); len(rates) != 0 {
			t.Fatalf("speed %v, caught up: legit first placements failed: %v", speed, rates)
		}
		old := randomFirstPlacements(t, uint64(i), speed, false)
		t.Logf("speed %v without the caught-up moves: failure rates %v", speed, old)
		if speed > 0.2 && old["scaffold.cursor"] < 0.2 {
			t.Fatalf("speed %v: the fixture should reproduce the old stream's mismatches: %v", speed, old)
		}
	}
}
