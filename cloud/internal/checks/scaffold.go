package checks

import (
	"fmt"
	"math"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// scaffold.face, scaffold.ray, scaffold.cursor and scaffold.reach (CHECKS.md 2.12).
//
// A placement waits for the next movement packet (at most 100 ms) to get its R1 rotation and the next
// eye position; then every check runs over the candidate eyes (event eye, the standing/sneaking
// alternative, the next move's eyes) and rotations (R0, R1 and the 0.25-step interpolations). The
// machinery (blockAction, blockQueue) is shared with dig.ray.

const (
	idScaffoldFace   = "scaffold.face"
	idScaffoldRay    = "scaffold.ray"
	idScaffoldCursor = "scaffold.cursor"
	idScaffoldReach  = "scaffold.reach"
)

func init() {
	Register(Factory{
		Name:   "scaffold",
		Checks: []string{idScaffoldFace, idScaffoldRay, idScaffoldCursor, idScaffoldReach},
		New:    func() Detector { return &scaffoldDetector{} },
	})
}

// protocol1_11 is the first client protocol that sends the placement cursor as floats (bytes before).
const protocol1_11 = 315

type scaffoldDetector struct {
	q      blockQueue
	ray    outcomeBits // true = miss
	cursor outcomeBits // true = mismatch
}

func (d *scaffoldDetector) Handle(c *Ctx, ev *pb.Event) {
	if j, ok := ev.GetKind().(*pb.Event_Join); ok && !j.Join.GetUpdate() {
		*d = scaffoldDetector{}
		return
	}
	d.q.handle(c, ev, d.judge)
	if p, ok := ev.GetKind().(*pb.Event_Place); ok {
		pl := p.Place
		if pl.GetInsideBlock() {
			return // 1.19+ "inside block" placements (head inside a slab, ...) are not judged
		}
		b := pl.GetBlock()
		d.q.add(c, blockAction{
			block:    [3]int32{b.GetX(), b.GetY(), b.GetZ()},
			face:     pl.GetFace(),
			cursor:   geom.V(float64(pl.GetCursorX()), float64(pl.GetCursorY()), float64(pl.GetCursorZ())),
			eye:      vec3(pl.GetEye()),
			rot:      geom.Rotation{Yaw: float64(pl.GetYaw()), Pitch: float64(pl.GetPitch())},
			sneaking: pl.GetSneaking(),
			reach:    float64(pl.GetBlockReach()),
		})
	}
}

func (d *scaffoldDetector) judge(c *Ctx, a *blockAction, cand *blockCandidates) {
	v := c.View
	bx, by, bz := a.block[0], a.block[1], a.block[2]
	cube := geom.BlockBox(bx, by, bz)

	// scaffold.face: the clicked face must be visible from some eye (superset over block shapes).
	if a.face != pb.Face_FACE_OTHER {
		u := c.Param(idScaffoldFace, "margin", 0.03) + geom.EyeUncertainty(v.Protocol)
		visible := false
		for _, e := range cand.eyes() {
			visible = visible || faceVisible(a.face, e, cube.Min, u)
		}
		if !visible {
			e := cand.eyes()[0]
			c.Emit(Emission{Check: idScaffoldFace, Points: 8, Confidence: 0.95, EventTimeUs: a.eventUs,
				Info: fmt.Sprintf("clicked the %s face of %d,%d,%d from an eye that cannot see it", faceName(a.face), bx, by, bz),
				Data: map[string]float64{"face": float64(a.face), "eye_x": e.X - float64(bx), "eye_y": e.Y - float64(by), "eye_z": e.Z - float64(bz)}})
		}
	}

	// scaffold.ray: some (eye, rotation) ray must hit the expanded cube within reach + 1.5.
	reach := a.reachOr(v)
	miss := !cand.rayHits(cube.Expand(c.Param(idScaffoldRay, "expand", 0.05)), reach+c.Param(idScaffoldRay, "extra_reach", 1.5))
	d.ray.push(miss)
	if n := d.ray.count(int(c.Param(idScaffoldRay, "of", 10))); miss && float64(n) >= c.Param(idScaffoldRay, "need", 3) {
		d.ray.reset()
		c.Emit(Emission{Check: idScaffoldRay, Points: 6, Confidence: 0.85, EventTimeUs: a.eventUs,
			Info: fmt.Sprintf("%d of the last placements were not in view of any rotation", n),
			Data: map[string]float64{"missed": float64(n), "rotations": float64(cand.nRot)}})
	}

	// scaffold.cursor: the click position must lie on some look ray (3 degrees).
	if v.Protocol >= protocol1_11 && cursorInRange(a.cursor) {
		p := cube.Min.Add(a.cursor)
		near := false
		for _, e := range cand.eyes() {
			near = near || p.Dist(e) < c.Param(idScaffoldCursor, "min_dist", 0.3)
		}
		if !near {
			best := cand.minAngleTo(p)
			mismatch := best > c.Param(idScaffoldCursor, "max_angle", 3)
			d.cursor.push(mismatch)
			if n := d.cursor.count(int(c.Param(idScaffoldCursor, "of", 10))); mismatch && float64(n) >= c.Param(idScaffoldCursor, "need", 3) {
				d.cursor.reset()
				c.Emit(Emission{Check: idScaffoldCursor, Points: 5, Confidence: 0.8, EventTimeUs: a.eventUs,
					Info: fmt.Sprintf("click position %.1f degrees off every look ray (%d of the last placements)", best, n),
					Data: map[string]float64{"angle": best, "mismatched": float64(n)}})
			}
		}
	}

	// scaffold.reach: distance from every eye to the clicked cube.
	dist := math.Inf(1)
	for _, e := range cand.eyes() {
		dist = math.Min(dist, cube.DistanceTo(e))
	}
	limit := reach + c.Param(idScaffoldReach, "extra_reach", 0.6)
	if excess := dist - limit; excess > 0 {
		c.Emit(Emission{Check: idScaffoldReach, Points: 3 + 5*math.Min(excess, 1), Confidence: 0.8, EventTimeUs: a.eventUs,
			Info: fmt.Sprintf("placed %.2f blocks away (limit %.2f)", dist, limit),
			Data: map[string]float64{"distance": dist, "limit": limit, "excess": excess}})
	}
}

// faceVisible applies the face inequalities of CHECKS.md 2.12 for the block whose minimum corner is b.
func faceVisible(face pb.Face, e, b geom.Vec3, u float64) bool {
	switch face {
	case pb.Face_FACE_UP:
		return e.Y > b.Y-u
	case pb.Face_FACE_DOWN:
		return e.Y < b.Y+1+u
	case pb.Face_FACE_EAST:
		return e.X > b.X-u
	case pb.Face_FACE_WEST:
		return e.X < b.X+1+u
	case pb.Face_FACE_SOUTH:
		return e.Z > b.Z-u
	case pb.Face_FACE_NORTH:
		return e.Z < b.Z+1+u
	}
	return true
}

func faceName(f pb.Face) string {
	switch f {
	case pb.Face_FACE_UP:
		return "top"
	case pb.Face_FACE_DOWN:
		return "bottom"
	case pb.Face_FACE_EAST:
		return "east"
	case pb.Face_FACE_WEST:
		return "west"
	case pb.Face_FACE_SOUTH:
		return "south"
	case pb.Face_FACE_NORTH:
		return "north"
	}
	return "other"
}

func cursorInRange(c geom.Vec3) bool {
	in := func(x float64) bool { return x >= 0 && x <= 1 }
	return in(c.X) && in(c.Y) && in(c.Z)
}

func vec3(v *pb.Vec3) geom.Vec3 { return geom.V(v.GetX(), v.GetY(), v.GetZ()) }

// ---------------------------------------------------------------------------------------------
// Block interactions waiting for their R1 (shared with dig.ray).
// ---------------------------------------------------------------------------------------------

// blockAction is a placement or a dig as the plugin reported it.
type blockAction struct {
	eventUs  uint64 // plugin event time (for the emission)
	atUs     int64  // normalised time
	tick     uint32
	block    [3]int32
	face     pb.Face
	cursor   geom.Vec3
	eye      geom.Vec3
	rot      geom.Rotation // R0: the last rotation received before the packet
	sneaking bool
	pose     geom.Pose // pose the plugin used for eye
	reach    float64
}

func (a *blockAction) reachOr(v *View) float64 {
	if a.reach > 0 {
		return a.reach
	}
	return v.BlockReach
}

// blockQueueCap bounds the actions waiting for their next move.
const blockQueueCap = 16

// blockActionWaitUs is how long an action waits for the next movement packet (CHECKS.md 2.1).
const blockActionWaitUs = 100_000

// blockQueue holds actions until the next movement packet (or 100 ms) supplies R1 and the next eye.
type blockQueue struct {
	items [blockQueueCap]blockAction
	n     int
}

// add queues a, filling in the event fields, unless the action must be skipped (CHECKS.md 2.1: not
// judgeable, ping > 600 ms, the second after a teleport, a server-set rotation).
func (q *blockQueue) add(c *Ctx, a blockAction) {
	v := c.View
	if !v.Judgeable() || v.PingMs > 600 || v.Since(v.LastTeleportUs) < time1s || !a.eye.IsFinite() ||
		!geom.V(a.rot.Yaw, a.rot.Pitch, 0).IsFinite() || v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) != 0 {
		return // NaN rotations are protocol.position's business (local)
	}
	a.atUs, a.tick = v.NowUs, v.Tick
	if c.event != nil {
		a.eventUs = c.event.GetTimeUs()
	}
	a.pose = geom.PoseStanding
	switch {
	case v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_SWIMMING|pb.MoveFlag_MOVE_FLAG_GLIDING) != 0:
		a.pose = geom.PoseSwimming
	case a.sneaking:
		a.pose = geom.PoseSneaking
	}
	if q.n == blockQueueCap {
		copy(q.items[:], q.items[1:])
		q.n--
	}
	q.items[q.n] = a
	q.n++
}

// handle resolves queued actions: on a movement packet with that move, otherwise after 100 ms without it.
// judge runs for every resolved action that is still judgeable.
func (q *blockQueue) handle(c *Ctx, ev *pb.Event, judge func(*Ctx, *blockAction, *blockCandidates)) {
	if q.n == 0 {
		return
	}
	v := c.View
	_, isMove := ev.GetKind().(*pb.Event_Move)
	var next *Move
	if isMove {
		if m, ok := v.Moves.Last(); ok {
			next = &m
		}
	}
	keep := 0
	for i := 0; i < q.n; i++ {
		a := q.items[i]
		timedOut := v.NowUs-a.atUs > blockActionWaitUs
		if !isMove && !timedOut {
			q.items[keep] = a
			keep++
			continue
		}
		if !v.Judgeable() || v.LastTeleportUs >= a.atUs && v.LastTeleportUs != 0 {
			continue
		}
		m := next
		if timedOut {
			m = nil
		}
		if m != nil && m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) {
			continue // the server set the next rotation: R1 is not the player's
		}
		var cand blockCandidates
		cand.build(v, &a, m)
		judge(c, &a, &cand)
	}
	q.n = keep
}

// blockCandidates are the eye and rotation candidates of one action.
type blockCandidates struct {
	eye  [5]geom.Vec3
	nEye int
	rot  [5]geom.Rotation
	nRot int
}

func (b *blockCandidates) eyes() []geom.Vec3 { return b.eye[:b.nEye] }

func (b *blockCandidates) rots() []geom.Rotation { return b.rot[:b.nRot] }

func (b *blockCandidates) addEye(e geom.Vec3) {
	if b.nEye < len(b.eye) && e.IsFinite() {
		b.eye[b.nEye] = e
		b.nEye++
	}
}

// build derives the candidates: eyes = the event eye, the standing/sneaking alternative at the same feet,
// and the next move's standing/sneaking eyes; rotations = R0, R0->R1 at 0.25 steps and R1. R1 is the next
// move's rotation when it belongs to the same client tick (tick clients) or arrived within 100 ms (older
// clients); otherwise R1 = R0.
func (b *blockCandidates) build(v *View, a *blockAction, next *Move) {
	stand := geom.EyeHeight(geom.PoseStanding, v.Protocol, v.Scale)
	sneak := geom.EyeHeight(geom.PoseSneaking, v.Protocol, v.Scale)
	used := geom.EyeHeight(a.pose, v.Protocol, v.Scale)
	feet := a.eye.Sub(geom.V(0, used, 0))
	b.addEye(a.eye)
	for _, h := range [2]float64{stand, sneak} {
		if h != used {
			b.addEye(feet.Add(geom.V(0, h, 0)))
		}
	}
	r1 := a.rot
	if next != nil && next.TimeUs-a.atUs <= blockActionWaitUs {
		if next.HasPos {
			b.addEye(next.Pos.Add(geom.V(0, stand, 0)))
			b.addEye(next.Pos.Add(geom.V(0, sneak, 0)))
		}
		if next.HasRot && (!v.TickClient || next.Tick == a.tick) {
			r1 = next.Rot
		}
	}
	for _, r := range geom.RotationCandidates(a.rot, r1) {
		b.rot[b.nRot] = r
		b.nRot++
	}
}

// rayHits reports whether some (eye, rotation) ray hits box within maxDist.
func (b *blockCandidates) rayHits(box geom.AABB, maxDist float64) bool {
	for _, e := range b.eyes() {
		for _, r := range b.rots() {
			if _, hit := box.IntersectRay(e, r.Look(), maxDist); hit {
				return true
			}
		}
	}
	return false
}

// minAngleTo is the smallest angle (degrees) between a candidate look vector and the direction from its
// eye to p.
func (b *blockCandidates) minAngleTo(p geom.Vec3) float64 {
	best := math.Inf(1)
	for _, e := range b.eyes() {
		for _, r := range b.rots() {
			if a, ok := geom.AngleBetween(r.Look(), p.Sub(e)); ok && a < best {
				best = a
			}
		}
	}
	return best
}
