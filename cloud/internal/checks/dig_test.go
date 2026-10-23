package checks_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// mine digs block b looking at the centre of the given face: START, a swing per tick for ticks ticks,
// FINISH (or CANCEL), as a vanilla client does. rot overrides the rotation (cheats).
func mine(r *rig, b [3]int32, face pb.Face, ticks int, finish bool, rot *geom.Rotation) {
	eye := r.eyeOf(false)
	centre := geom.BlockBox(b[0], b[1], b[2]).Center()
	n := map[pb.Face]geom.Vec3{pb.Face_FACE_UP: {Y: 0.5}, pb.Face_FACE_DOWN: {Y: -0.5}, pb.Face_FACE_NORTH: {Z: -0.5},
		pb.Face_FACE_SOUTH: {Z: 0.5}, pb.Face_FACE_WEST: {X: -0.5}, pb.Face_FACE_EAST: {X: 0.5}}[face]
	look := aim(eye, centre.Add(n))
	if rot != nil {
		look = *rot
	}
	r.tick(r.p.Pos, look)
	c := blockClick{block: b, face: face, eye: eye, rot: look}
	r.dig(pb.BlockDig_ACTION_START, c)
	r.swing(pb.Hand_HAND_MAIN)
	r.tick(r.p.Pos, look)
	for range ticks {
		r.swing(pb.Hand_HAND_MAIN)
		r.tick(r.p.Pos, look)
	}
	action := pb.BlockDig_ACTION_CANCEL
	if finish {
		action = pb.BlockDig_ACTION_FINISH
	}
	r.dig(action, c)
	r.tick(r.p.Pos, look)
}

func TestDigLegitMining(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for x := int32(-2); x <= 2; x++ {
		mine(r, [3]int32{x, 64, 2}, pb.Face_FACE_NORTH, 6, true, nil)  // a wall in front
		mine(r, [3]int32{x, 63, 1}, pb.Face_FACE_UP, 6, true, nil)     // the floor
		mine(r, [3]int32{x, 66, 1}, pb.Face_FACE_DOWN, 3, false, nil)  // the ceiling, aborted
		mine(r, [3]int32{x, 65, -2}, pb.Face_FACE_SOUTH, 2, true, nil) // behind
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("dig"); len(es) != 0 {
		t.Fatalf("legit mining: %+v", es)
	}
}

func TestDigLegitMiningAfterCombat(t *testing.T) {
	// Hit a mob, then mine obsidian (long dig) and a cobweb, looking at them.
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for range 5 {
		r.attack()
		r.swing(pb.Hand_HAND_MAIN)
		r.tick(r.p.Pos, geom.Rotation{Yaw: 30})
		mine(r, [3]int32{0, 64, 3}, pb.Face_FACE_NORTH, 60, true, nil)
		mine(r, [3]int32{1, 65, 2}, pb.Face_FACE_NORTH, 8, true, nil)
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("dig"); len(es) != 0 {
		t.Fatalf("mining after combat: %+v", es)
	}
	if es := r.run("autoclicker"); len(es) != 0 {
		t.Fatalf("mining after combat (autoclicker): %+v", es)
	}
}

func TestDigLegitWhileTurning(t *testing.T) {
	// The START's R0 lags behind; the same tick's movement packet carries the rotation the client used.
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := range 12 {
		b := [3]int32{int32(i%5 - 2), 64, 3}
		eye := r.eyeOf(false)
		r1 := aim(eye, geom.V(float64(b[0])+0.5, 64.5, 3))
		r0 := geom.Rotation{Yaw: r1.Yaw - 60, Pitch: r1.Pitch + 20}
		r.tick(r.p.Pos, r0)
		r.dig(pb.BlockDig_ACTION_START, blockClick{block: b, face: pb.Face_FACE_NORTH, eye: eye, rot: r0})
		r.tick(r.p.Pos, r1)
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("dig"); len(es) != 0 {
		t.Fatalf("R1 must be a candidate: %+v", es)
	}
}

// A crawler mines the block ahead and the floor in front of it: only the 0.4 eye sees them.
func TestDigLegitCrawling(t *testing.T) {
	r := newRig(t, rigProtocol)
	crawlTunnel(r, 20, func(i int, eye, reported geom.Vec3) {
		z := int32(math.Floor(eye.Z)) - 1
		c := blockClick{block: [3]int32{0, 64, z}, face: pb.Face_FACE_SOUTH, eye: reported, rot: r.p.Rot}
		look := aim(eye, geom.V(0.5, 64.5, float64(z)+1)) // the south face ahead
		if i%2 == 1 {
			c.block, c.face = [3]int32{0, 63, z}, pb.Face_FACE_UP
			look = aim(eye, geom.V(0.5, 64, float64(z)+0.5)) // the floor's top face
		}
		for _, h := range []float64{1.27, 1.62} {
			if _, hit := geom.BlockBox(c.block[0], c.block[1], c.block[2]).Expand(0.05).IntersectRay(eye.Add(geom.V(0, h-0.4, 0)), look.Look(), 6); hit {
				t.Fatalf("fixture: the %.2f eye sees %v", h, c.block)
			}
		}
		r.dig(pb.BlockDig_ACTION_START, c)
		r.tick(r.p.Pos, look)
		c.rot = look
		r.dig(pb.BlockDig_ACTION_FINISH, c)
		r.tick(r.p.Pos, look)
	})
	if es := r.run("dig"); len(es) != 0 {
		t.Fatalf("legit crawling digs: %+v", es)
	}
}

func TestDigCheatNuker(t *testing.T) {
	// Breaks every block around the player while looking straight ahead.
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	ahead := geom.Rotation{Yaw: 0, Pitch: 0}
	n := 0
	for x := int32(-3); x <= 3; x++ {
		for z := int32(-3); z <= 3; z++ {
			if x == 0 {
				continue // the column the player looks along
			}
			b := [3]int32{x, 63, z}
			c := blockClick{block: b, face: pb.Face_FACE_UP, eye: r.eyeOf(false), rot: ahead}
			r.dig(pb.BlockDig_ACTION_START, c)
			r.dig(pb.BlockDig_ACTION_FINISH, c)
			r.tick(r.p.Pos, ahead)
			n += 2
		}
	}
	r.step(geom.V(0, 0, 0))
	es := r.run("dig")
	expectCount(t, es, "dig.ray", n/4)
	expectScoring(t, es, "dig.ray", 5, 5, 0.85)
}

func TestDigSkipsCancelAndCreative(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	ahead := geom.Rotation{}
	for range 12 {
		r.dig(pb.BlockDig_ACTION_CANCEL, blockClick{block: [3]int32{5, 60, 5}, face: pb.Face_FACE_UP, eye: r.eyeOf(false), rot: ahead})
		r.tick(r.p.Pos, ahead)
	}
	r.state(func(s *pb.StateChange) { s.GameMode = pb.GameMode_GAME_MODE_CREATIVE })
	for range 12 {
		r.dig(pb.BlockDig_ACTION_START, blockClick{block: [3]int32{5, 60, 5}, face: pb.Face_FACE_UP, eye: r.eyeOf(false), rot: ahead})
		r.tick(r.p.Pos, ahead)
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("dig"); len(es) != 0 {
		t.Fatalf("CANCEL and creative digs are not judged: %+v", es)
	}
}

func TestDigPropertyRandomLegit(t *testing.T) {
	for seed := range uint64(10) {
		rng := rand.New(rand.NewPCG(seed, 3))
		r := newRig(t, rigProtocol)
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
		for i := 0; i < 200; {
			r.step(geom.V(rng.Float64()*0.4-0.2, 0, rng.Float64()*0.4-0.2))
			eye := r.eyeOf(false)
			rot := geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: rng.Float64()*180 - 90}
			p := eye.Add(rot.Look().Scale(0.8 + rng.Float64()*3.6))
			b := [3]int32{int32(math.Floor(p.X)), int32(math.Floor(p.Y)), int32(math.Floor(p.Z))}
			face, _, ok := rayClick(eye, rot, b)
			if !ok {
				continue
			}
			r0 := geom.Rotation{Yaw: rot.Yaw + rng.Float64()*60 - 30, Pitch: rot.Pitch}
			r.tick(r.p.Pos, r0)
			c := blockClick{block: b, face: face, eye: eye, rot: r0}
			r.dig(pb.BlockDig_ACTION_START, c)
			r.tick(r.p.Pos, rot)
			for range rng.IntN(5) {
				r.swing(pb.Hand_HAND_MAIN)
				r.tick(r.p.Pos, rot)
			}
			c.rot = rot
			r.dig(pb.BlockDig_ACTION_FINISH, c)
			r.tick(r.p.Pos, rot)
			i++
		}
		r.step(geom.V(0, 0, 0))
		if es := r.run("dig"); len(es) != 0 {
			t.Fatalf("seed %d: %+v", seed, es[0])
		}
	}
}
