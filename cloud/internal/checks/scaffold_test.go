package checks_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// blockClick is a placement (or dig) as the plugin reports it.
type blockClick struct {
	block  [3]int32
	face   pb.Face
	cursor geom.Vec3
	eye    geom.Vec3
	rot    geom.Rotation // R0
	sneak  bool
	inside bool
	reach  float32
}

func (r *rig) place(c blockClick) {
	reach := c.reach
	if reach == 0 {
		reach = 4.5
	}
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Place{Place: &pb.BlockPlace{
			Block: &pb.Vec3I{X: c.block[0], Y: c.block[1], Z: c.block[2]}, Face: c.face,
			CursorX: float32(c.cursor.X), CursorY: float32(c.cursor.Y), CursorZ: float32(c.cursor.Z),
			Eye: &pb.Vec3{X: c.eye.X, Y: c.eye.Y, Z: c.eye.Z}, Yaw: float32(c.rot.Yaw), Pitch: float32(c.rot.Pitch),
			Sneaking: c.sneak, BlockReach: reach, InsideBlock: c.inside, Item: "minecraft:cobblestone"}}
	})
}

func (r *rig) dig(action pb.BlockDig_Action, c blockClick) {
	reach := c.reach
	if reach == 0 {
		reach = 4.5
	}
	r.emit(func(e *pb.Event) {
		e.Kind = &pb.Event_Dig{Dig: &pb.BlockDig{Action: action, Block: &pb.Vec3I{X: c.block[0], Y: c.block[1], Z: c.block[2]},
			Face: c.face, Eye: &pb.Vec3{X: c.eye.X, Y: c.eye.Y, Z: c.eye.Z}, Yaw: float32(c.rot.Yaw), Pitch: float32(c.rot.Pitch),
			BlockReach: reach}}
	})
}

// eyeOf is the eye the plugin reports for the rig's current position.
func (r *rig) eyeOf(sneak bool) geom.Vec3 {
	pose := geom.PoseStanding
	if sneak {
		pose = geom.PoseSneaking
	}
	return geom.Eye(r.p.Pos, pose, r.p.Protocol, 1)
}

func aim(from, to geom.Vec3) geom.Rotation {
	rot, ok := geom.Angles(to.Sub(from))
	if !ok {
		panic("aim at the eye itself")
	}
	return rot
}

// rayClick computes what a vanilla client sends when its crosshair ray from eye along rot enters block b:
// the entry face and the cursor (hit point relative to b). ok is false when the ray misses b.
func rayClick(eye geom.Vec3, rot geom.Rotation, b [3]int32) (face pb.Face, cursor geom.Vec3, ok bool) {
	box := geom.BlockBox(b[0], b[1], b[2])
	dir := rot.Look()
	t, hit := box.IntersectRay(eye, dir, math.Inf(1))
	if !hit || t == 0 {
		return 0, geom.Vec3{}, false
	}
	p := eye.Add(dir.Scale(t))
	c := p.Sub(box.Min)
	const e = 1e-7
	switch {
	case math.Abs(c.Y-1) < e:
		face = pb.Face_FACE_UP
	case math.Abs(c.Y) < e:
		face = pb.Face_FACE_DOWN
	case math.Abs(c.X-1) < e:
		face = pb.Face_FACE_EAST
	case math.Abs(c.X) < e:
		face = pb.Face_FACE_WEST
	case math.Abs(c.Z-1) < e:
		face = pb.Face_FACE_SOUTH
	default:
		face = pb.Face_FACE_NORTH
	}
	return face, c, true
}

// bridge simulates sneak-bridging north (-z): the player stands at the edge of the newest block, looks
// at the centre of its north face, places against it, and walks onto the new block. rotOf can replace
// the rotation (cheats).
func bridge(r *rig, n int, rotOf func(aimed geom.Rotation) geom.Rotation) {
	r.flags = uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING)
	r.tick(geom.V(0.5, 64, 0.25), geom.Rotation{Yaw: 0, Pitch: 80})
	for i := range n {
		zb := int32(-i)
		b := [3]int32{0, 63, zb}
		eye := r.eyeOf(true)
		target := geom.V(0.5, 63.5, float64(zb))
		aimed := aim(eye, target)
		rot := aimed
		if rotOf != nil {
			rot = rotOf(aimed)
		}
		r.tick(r.p.Pos, rot) // turn (R0 of the placement)
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: target.Sub(geom.V(0, 63, float64(zb))),
			eye: r.eyeOf(true), rot: rot, sneak: true})
		r.tick(r.p.Pos, rot)
		for range 4 {
			r.step(geom.V(0, 0, -0.25))
		}
	}
	r.step(geom.V(0, 0, 0))
}

func TestScaffoldLegitBridging(t *testing.T) {
	r := newRig(t, rigProtocol)
	bridge(r, 30, nil)
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("legit bridging: %+v", es)
	}
}

func TestScaffoldCheatNoRotation(t *testing.T) {
	r := newRig(t, rigProtocol)
	bridge(r, 12, func(geom.Rotation) geom.Rotation { return geom.Rotation{Yaw: 0, Pitch: 0} }) // looks south, places north
	es := r.run("scaffold")
	expectCount(t, es, "scaffold.ray", 4) // a violation every 3 misses
	expectCount(t, es, "scaffold.cursor", 4)
	expectCount(t, es, "scaffold.face", 0)
	expectCount(t, es, "scaffold.reach", 0)
	expectScoring(t, es, "scaffold.ray", 6, 6, 0.85)
	expectScoring(t, es, "scaffold.cursor", 5, 5, 0.8)
}

func TestScaffoldLegitBuildingWhileTurning(t *testing.T) {
	// Tick clients pick the block with the rotation of the same tick's movement packet (R1): the
	// placement's R0 still has the old rotation, 70 degrees off.
	build := func(sameTick bool) []*pb.Event {
		r := newRig(t, rigProtocol)
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
		for i := range 12 {
			b := [3]int32{int32(2 + i%3), 64, 3}
			eye := r.eyeOf(false)
			target := geom.V(float64(b[0])+0.5, 64.5, 3)
			r1 := aim(eye, target)
			r0 := geom.Rotation{Yaw: r1.Yaw + 70, Pitch: r1.Pitch}
			r.tick(r.p.Pos, r0)
			r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: target.Sub(geom.V(float64(b[0]), 64, 3)), eye: eye, rot: r0})
			if !sameTick {
				r.idle()
			}
			r.tick(r.p.Pos, r1)
			r.step(geom.V(0, 0, 0))
		}
		return r.events
	}
	r := newRig(t, rigProtocol)
	r.events = build(true)
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("R1 of the same tick must be a candidate: %+v", es)
	}
	r.events = build(false)
	expectAtLeast(t, r.run("scaffold"), "scaffold.ray", 1) // a later tick's rotation is not R1
}

func TestScaffoldLegitTurningLegacyClient(t *testing.T) {
	// Clients without tick numbers: R1 is the next move within 100 ms.
	r := newRig(t, 340)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := range 12 {
		b := [3]int32{int32(2 + i%3), 64, 3}
		eye := r.eyeOf(false)
		target := geom.V(float64(b[0])+0.5, 64.5, 3)
		r1 := aim(eye, target)
		r0 := geom.Rotation{Yaw: r1.Yaw + 70, Pitch: r1.Pitch}
		r.tick(r.p.Pos, r0)
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: target.Sub(geom.V(float64(b[0]), 64, 3)), eye: eye, rot: r0})
		r.wait(30_000_000) // 30 ms
		r.tick(r.p.Pos, r1)
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("legacy R1: %+v", es)
	}
}

func TestScaffoldLegitInsideBlock(t *testing.T) {
	// Head inside a slab (1.19+ inside_block): nothing is judged, even with geometry that looks wrong.
	play := func(inside bool) []*pb.Event {
		r := newRig(t, rigProtocol)
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{Yaw: 0, Pitch: 0})
		for range 12 {
			r.place(blockClick{block: [3]int32{0, 65, 0}, face: pb.Face_FACE_DOWN, cursor: geom.V(0.95, 0.95, 0.05),
				eye: r.eyeOf(false), rot: geom.Rotation{Yaw: 0, Pitch: -10}, inside: inside})
			r.step(geom.V(0, 0, 0))
		}
		return r.events
	}
	r := newRig(t, rigProtocol)
	r.events = play(true)
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("inside_block placements must be skipped: %+v", es)
	}
	r.events = play(false)
	expectAtLeast(t, r.run("scaffold"), "scaffold.cursor", 1)
}

func TestScaffoldLegitSneakToggledInTheSameTick(t *testing.T) {
	// Shift is pressed and the block placed in the same tick. The client's ray starts at the sneaking
	// eye, but the plugin's sneaking state (and eye) still says standing. The player stands still, so no
	// movement packet follows within 100 ms: only the event eye and its alternative are candidates.
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := range 12 {
		b := [3]int32{int32(i%3 - 1), 63, 1}
		target := geom.V(float64(b[0])+0.5, 64, 1.5)
		rot := aim(r.eyeOf(true), target)
		r.tick(r.p.Pos, rot)
		r.place(blockClick{block: b, face: pb.Face_FACE_UP, cursor: target.Sub(geom.V(float64(b[0]), 63, 1)), eye: r.eyeOf(false), rot: rot})
		for range 3 {
			r.idle()
		}
		r.flags = uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING)
		r.tick(r.p.Pos, rot)
		r.flags = 0
	}
	r.step(geom.V(0, 0, 0))
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("the sneaking eye must be a candidate: %+v", es)
	}
}

func TestScaffoldLegacyCursorBytes(t *testing.T) {
	// Before 1.11 the cursor is sent as bytes; ViaVersion-translated values do not lie on the look ray.
	run := func(protocol int32) int {
		r := newRig(t, protocol)
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
		for i := range 12 {
			b := [3]int32{int32(i%3 - 1), 63, 2}
			eye := r.eyeOf(false)
			target := geom.V(float64(b[0])+0.5, 64, 2.5)
			rot := aim(eye, target)
			r.tick(r.p.Pos, rot)
			r.place(blockClick{block: b, face: pb.Face_FACE_UP, cursor: geom.V(0.9375, 1, 0.0625), eye: eye, rot: rot})
			r.tick(r.p.Pos, rot)
		}
		r.step(geom.V(0, 0, 0))
		es := r.run("scaffold")
		expectCount(t, es, "scaffold.ray", 0)
		expectCount(t, es, "scaffold.face", 0)
		return len(es)
	}
	if n := run(47); n != 0 { // 1.8
		t.Fatalf("legacy cursor judged: %d emissions", n)
	}
	if n := run(rigProtocol); n == 0 {
		t.Fatal("the same cursors from a modern client must mismatch")
	}
}

func TestScaffoldLegitLegacySneakEyeHeight(t *testing.T) {
	// A 1.12 client sneaks at 1.54; the plugin reports that eye.
	r := newRig(t, 340)
	bridge(r, 20, nil)
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("legacy sneak eye height: %+v", es)
	}
}

func TestScaffoldCheatImpossibleFace(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for range 5 {
		// The bottom face of the block the player stands on cannot be seen from above it.
		eye := r.eyeOf(false)
		target := geom.V(0.5, 63, 0.5)
		rot := aim(eye, target)
		r.tick(r.p.Pos, rot)
		r.place(blockClick{block: [3]int32{0, 63, 0}, face: pb.Face_FACE_DOWN, cursor: geom.V(0.5, 0, 0.5), eye: eye, rot: rot})
		r.tick(r.p.Pos, rot)
	}
	r.step(geom.V(0, 0, 0))
	es := r.run("scaffold")
	expectCount(t, es, "scaffold.face", 5)
	expectCount(t, es, "scaffold.ray", 0)
	expectCount(t, es, "scaffold.cursor", 0)
	expectScoring(t, es, "scaffold.face", 8, 8, 0.95)
}

func TestScaffoldCheatReach(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for range 4 {
		b := [3]int32{0, 64, 7} // 6.5 blocks in front of the eye
		eye := r.eyeOf(false)
		target := geom.V(0.5, 64.5, 7)
		rot := aim(eye, target)
		r.tick(r.p.Pos, rot)
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: geom.V(0.5, 0.5, 0), eye: eye, rot: rot})
		r.tick(r.p.Pos, rot)
	}
	r.step(geom.V(0, 0, 0))
	es := r.run("scaffold")
	expectCount(t, es, "scaffold.reach", 4)
	expectCount(t, es, "scaffold.face", 0)
	expectScoring(t, es, "scaffold.reach", 3, 8, 0.8)
	for _, e := range es {
		if e.Check == "scaffold.reach" && math.Abs(e.Points-(3+5*math.Min(e.Data["excess"], 1))) > 1e-9 {
			t.Fatalf("reach points %+v", e)
		}
	}
	// Just inside block_reach + 0.6: legit.
	ok := newRig(t, rigProtocol)
	ok.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for range 4 {
		b := [3]int32{0, 64, 5} // 4.5 blocks away (the client allows 4.5 from the eye)
		eye := ok.eyeOf(false)
		target := geom.V(0.5, 64.5, 5)
		rot := aim(eye, target)
		ok.tick(ok.p.Pos, rot)
		ok.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: geom.V(0.5, 0.5, 0), eye: eye, rot: rot})
		ok.tick(ok.p.Pos, rot)
	}
	ok.step(geom.V(0, 0, 0))
	if es := ok.run("scaffold"); len(es) != 0 {
		t.Fatalf("placement within reach: %+v", es)
	}
}

func TestScaffoldCheatCursorMismatch(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := range 9 {
		b := [3]int32{int32(i%3 - 1), 63, 2}
		eye := r.eyeOf(false)
		rot := aim(eye, geom.V(float64(b[0])+0.5, 64, 2.5))
		r.tick(r.p.Pos, rot)
		// The rotation hits the centre of the top face, the cursor claims a corner.
		r.place(blockClick{block: b, face: pb.Face_FACE_UP, cursor: geom.V(0.02, 1, 0.98), eye: eye, rot: rot})
		r.tick(r.p.Pos, rot)
	}
	r.step(geom.V(0, 0, 0))
	es := r.run("scaffold")
	expectCount(t, es, "scaffold.cursor", 3)
	expectCount(t, es, "scaffold.ray", 0)
	expectCount(t, es, "scaffold.face", 0)
}

func TestScaffoldSkips(t *testing.T) {
	cheat := func(r *rig, flags uint32) {
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
		for range 9 {
			r.flags = flags
			r.tick(r.p.Pos, geom.Rotation{Yaw: 0, Pitch: 0})
			r.flags = 0
			r.place(blockClick{block: [3]int32{0, 63, -1}, face: pb.Face_FACE_DOWN, cursor: geom.V(0.5, 0, 0.5),
				eye: r.eyeOf(false), rot: geom.Rotation{Yaw: 0, Pitch: 0}})
			r.tick(r.p.Pos, geom.Rotation{Yaw: 0, Pitch: 0})
		}
		r.step(geom.V(0, 0, 0))
	}
	t.Run("server rotation", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		cheat(r, uint32(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION))
		if es := r.run("scaffold"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("teleport", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		r.teleport()
		r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
		r.place(blockClick{block: [3]int32{0, 63, -1}, face: pb.Face_FACE_DOWN, eye: r.eyeOf(false)})
		r.step(geom.V(0, 0, 0))
		if es := r.run("scaffold"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("creative", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		r.state(func(s *pb.StateChange) { s.GameMode = pb.GameMode_GAME_MODE_CREATIVE })
		cheat(r, 0)
		if es := r.run("scaffold"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("control", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		cheat(r, 0)
		expectAtLeast(t, r.run("scaffold"), "scaffold.face", 9)
	})
}

// randomPlacements are vanilla-consistent clicks from random positions and rotations.
func randomPlacements(r *rig, rng *rand.Rand, n int) {
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := 0; i < n; {
		r.flags = 0
		sneak := rng.IntN(3) == 0
		if sneak {
			r.flags = uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING)
		}
		r.step(geom.V(rng.Float64()*0.4-0.2, 0, rng.Float64()*0.4-0.2))
		eye := r.eyeOf(sneak)
		rot := geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: rng.Float64()*180 - 90}
		d := 0.8 + rng.Float64()*3.6
		p := eye.Add(rot.Look().Scale(d))
		b := [3]int32{int32(math.Floor(p.X)), int32(math.Floor(p.Y)), int32(math.Floor(p.Z))}
		face, cursor, ok := rayClick(eye, rot, b)
		if !ok {
			continue
		}
		// Turning during the tick: R0 lags behind R1 by up to 40 degrees; the client used R1.
		r0 := geom.Rotation{Yaw: rot.Yaw + rng.Float64()*80 - 40, Pitch: math.Max(-90, math.Min(90, rot.Pitch+rng.Float64()*20-10))}
		r.tick(r.p.Pos, r0)
		r.place(blockClick{block: b, face: face, cursor: cursor, eye: eye, rot: r0, sneak: sneak})
		r.tick(r.p.Pos, rot)
		i++
	}
	r.step(geom.V(0, 0, 0))
}

func TestScaffoldPropertyRandomLegit(t *testing.T) {
	for seed := range uint64(10) {
		r := newRig(t, rigProtocol)
		randomPlacements(r, rand.New(rand.NewPCG(seed, 99)), 300)
		if es := r.run("scaffold"); len(es) != 0 {
			t.Fatalf("seed %d: %d emissions, first %+v", seed, len(es), es[0])
		}
	}
}

// Since 1.20 the client picks its target every frame from getEyePosition(partialTick): the eye lies
// between the previous tick's position and the last one sent, not at the last one. A player flying fast
// while building (/fly, elytra) clicks from up to a tick of movement behind the reported eye.
func TestScaffoldLegitInterpolatedEyeWhileMovingFast(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.state(func(s *pb.StateChange) { s.Flying, s.AllowFlight = true, true })
	r.tick(geom.V(0.5, 70, 0.5), geom.Rotation{})
	for i := range 24 {
		prev := r.p.Pos
		cur := prev.Add(geom.V(0.9, 0, 0))
		partial := []float64{0.05, 0.2, 0.5}[i%3]
		eye := geom.Eye(prev.Add(cur.Sub(prev).Scale(partial)), geom.PoseStanding, r.p.Protocol, 1)
		b := [3]int32{int32(math.Floor(prev.X)), 68, int32(-1 + i%3)}
		target := geom.V(float64(b[0])+0.5, 69, float64(b[2])+0.5) // top face centre
		rot := aim(eye, target)
		face, cursor, ok := rayClick(eye, rot, b)
		if !ok {
			t.Fatalf("setup: ray from the interpolated eye misses %v", b)
		}
		r.tick(cur, r.p.Rot) // P(N) is sent; the frame before the next tick picks from between prev and cur
		r.place(blockClick{block: b, face: face, cursor: cursor, eye: r.eyeOf(false), rot: r.p.Rot})
		r.tick(cur, rot) // the same tick's movement packet carries the pick rotation (R1)
	}
	if es := r.run("scaffold"); len(es) != 0 {
		t.Fatalf("legit fast building flagged: %d emissions, first %+v", len(es), es[0])
	}
}
