package checks

import (
	"fmt"
	"math"
	"slices"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// scaffold.face, scaffold.ray, scaffold.cursor and scaffold.reach (CHECKS.md 2.12).
//
// A placement waits for the next movement packet (at most 100 ms) to get its R1 rotation and the next
// eye position; then every check runs over the candidate eyes (event eye, the standing/sneaking
// alternative and 0.4 for a swimming/gliding pose, the next move's eyes) and rotations (R0, R1 and the
// 0.25-step interpolations). The machinery (blockAction, blockQueue) is shared with dig.ray. Every judged
// placement is also reported to the check-health digest (digest.go), once per check.

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
	obs := cand.observation(c, a)

	// scaffold.face: the clicked face must be visible from some eye (superset over block shapes).
	if a.face != pb.Face_FACE_OTHER {
		u := c.Param(idScaffoldFace, "margin", 0.03) + geom.EyeUncertainty(v.Protocol)
		margin := math.Inf(-1)
		for _, e := range cand.eyes() {
			margin = math.Max(margin, faceMargin(a.face, e, cube.Min, u))
		}
		o := obs
		o.Check, o.Value, o.Failed = idScaffoldFace, margin, !(margin > 0)
		if o.Failed {
			e := cand.eyes()[0]
			o.Emitted = c.publish(Emission{Check: idScaffoldFace, Points: 8, Confidence: 0.95, EventTimeUs: a.eventUs,
				Info: fmt.Sprintf("clicked the %s face of %d,%d,%d from an eye that cannot see it", faceName(a.face), bx, by, bz),
				Data: map[string]float64{"face": float64(a.face), "eye_x": e.X - float64(bx), "eye_y": e.Y - float64(by), "eye_z": e.Z - float64(bz)}})
		}
		c.observe(o)
	}

	// scaffold.ray: some (eye, rotation) ray must hit the expanded cube within reach + 1.5.
	reach := a.reachOr(v)
	box := cube.Expand(c.Param(idScaffoldRay, "expand", 0.05))
	miss := !cand.rayHits(box, reach+c.Param(idScaffoldRay, "extra_reach", 1.5))
	d.ray.push(miss)
	o := obs
	o.Check, o.Failed = idScaffoldRay, miss
	if miss && c.observing() {
		o.Value = cand.rayAngle(box)
	}
	if n := d.ray.count(int(c.Param(idScaffoldRay, "of", 10))); miss && float64(n) >= c.Param(idScaffoldRay, "need", 3) {
		d.ray.reset()
		o.Emitted = c.publish(Emission{Check: idScaffoldRay, Points: 6, Confidence: 0.85, EventTimeUs: a.eventUs,
			Info: fmt.Sprintf("%d of the last placements were not in view of any rotation", n),
			Data: map[string]float64{"missed": float64(n), "rotations": float64(cand.nRot)}})
	}
	c.observe(o)

	// scaffold.cursor: the click position must lie on some look ray (3 degrees).
	if v.Protocol >= protocol1_11 && cursorInRange(a.cursor) {
		p := cube.Min.Add(a.cursor)
		eyes := cand.cursorEyes(a.face, p, geom.EyeUncertainty(v.Protocol))
		near := false
		for _, e := range eyes {
			near = near || p.Dist(e) < c.Param(idScaffoldCursor, "min_dist", 0.3)
		}
		if !near {
			best := cand.minAngleTo(eyes, p)
			mismatch := best > c.Param(idScaffoldCursor, "max_angle", 3)
			d.cursor.push(mismatch)
			o := obs
			o.Check, o.Value, o.Failed, o.RayHit = idScaffoldCursor, best, mismatch, !miss
			if n := d.cursor.count(int(c.Param(idScaffoldCursor, "of", 10))); mismatch && float64(n) >= c.Param(idScaffoldCursor, "need", 3) {
				d.cursor.reset()
				o.Emitted = c.publish(Emission{Check: idScaffoldCursor, Points: 5, Confidence: 0.8, EventTimeUs: a.eventUs,
					Info: fmt.Sprintf("click position %.1f degrees off every look ray (%d of the last placements)", best, n),
					Data: map[string]float64{"angle": best, "mismatched": float64(n)}})
			}
			c.observe(o)
		}
	}

	// scaffold.reach: distance from every eye to the clicked cube.
	dist := math.Inf(1)
	for _, e := range cand.eyes() {
		dist = math.Min(dist, cube.DistanceTo(e))
	}
	limit := reach + c.Param(idScaffoldReach, "extra_reach", 0.6)
	excess := dist - limit
	o = obs
	o.Check, o.Value, o.Failed = idScaffoldReach, excess, excess > 0
	if excess > 0 {
		o.Emitted = c.publish(Emission{Check: idScaffoldReach, Points: 3 + 5*math.Min(excess, 1), Confidence: 0.8, EventTimeUs: a.eventUs,
			Info: fmt.Sprintf("placed %.2f blocks away (limit %.2f)", dist, limit),
			Data: map[string]float64{"distance": dist, "limit": limit, "excess": excess}})
	}
	c.observe(o)
}

// faceMargin is how far the eye e lies on the visible side of the clicked face of the block whose minimum
// corner is b, by the face inequalities of CHECKS.md 2.12: the face is visible when the margin is > 0 (for
// finite floats x - y > 0 exactly when x > y, so this is the inequality itself).
func faceMargin(face pb.Face, e, b geom.Vec3, u float64) float64 {
	switch face {
	case pb.Face_FACE_UP:
		return e.Y - (b.Y - u)
	case pb.Face_FACE_DOWN:
		return b.Y + 1 + u - e.Y
	case pb.Face_FACE_EAST:
		return e.X - (b.X - u)
	case pb.Face_FACE_WEST:
		return b.X + 1 + u - e.X
	case pb.Face_FACE_SOUTH:
		return e.Z - (b.Z - u)
	case pb.Face_FACE_NORTH:
		return b.Z + 1 + u - e.Z
	}
	return math.Inf(1)
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
	prevFeet geom.Vec3 // position before the last one (eyePath), when hasPrev
	hasPrev  bool
	action   pb.BlockDig_Action // dig.ray: START or FINISH
	gapUs    int64              // time since the queue's previous judged action, -1 for the first (set when judged)
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
	// lastUs is the time of the last judged action, when judged (the digest's gap)
	lastUs int64
	judged bool
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
	a.prevFeet, a.hasPrev = v.PrevFeet()
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
		a.gapUs = -1
		if q.judged {
			a.gapUs = max(a.atUs-q.lastUs, 0)
		}
		q.lastUs, q.judged = a.atUs, true
		judge(c, &a, &cand)
	}
	q.n = keep
}

// blockCandidates are the eye and rotation candidates of one action.
type blockCandidates struct {
	eye  [6 + 3*eyePathSamples]geom.Vec3
	nEye int
	rot  [5]geom.Rotation
	nRot int
	r1   geom.Rotation // R1 (R0 without one)
	// crawl holds the 0.4 (crawling) eye at every candidate feet when 0.4 is not a candidate height;
	// only scaffold.cursor uses them, for bottom-face clicks no other eye can see (cursorEyes).
	crawl  [2 + eyePathSamples]geom.Vec3
	nCrawl int
}

func (b *blockCandidates) eyes() []geom.Vec3 { return b.eye[:b.nEye] }

func (b *blockCandidates) rots() []geom.Rotation { return b.rot[:b.nRot] }

func (b *blockCandidates) addEye(e geom.Vec3) {
	if b.nEye < len(b.eye) && e.IsFinite() {
		b.eye[b.nEye] = e
		b.nEye++
	}
}

func (b *blockCandidates) addCrawlEye(e geom.Vec3) {
	if b.nCrawl < len(b.crawl) && e.IsFinite() {
		b.crawl[b.nCrawl] = e
		b.nCrawl++
	}
}

// build derives the candidates: eyes = the event eye, the other heights at the same feet, the eyes on
// the path from the previous position (eyePath) and the next move's eyes, each at the standing and
// sneaking heights plus 0.4 when the action's pose is swimming/gliding (a real crawl or swim, or a stale
// flag of a standing player); rotations = R0, R0->R1 at 0.25 steps and R1. R1 is the next
// move's rotation when it belongs to the same client tick (tick clients) or arrived within 100 ms (older
// clients); otherwise R1 = R0. When 0.4 is not among the heights (and the client has the 1.14 crawl
// pose), the 0.4 eyes at the same feet are kept apart for scaffold.cursor (cursorEyes).
func (b *blockCandidates) build(v *View, a *blockAction, next *Move) {
	stand := geom.EyeHeight(geom.PoseStanding, v.Protocol, v.Scale)
	sneak := geom.EyeHeight(geom.PoseSneaking, v.Protocol, v.Scale)
	used := geom.EyeHeight(a.pose, v.Protocol, v.Scale)
	all := [3]float64{stand, sneak, used}
	heights := all[:2]
	if used != stand && used != sneak {
		heights = all[:]
	}
	crawl := geom.EyeHeight(geom.PoseCrawling, v.Protocol, v.Scale)
	keepCrawl := used != crawl && v.Protocol >= geom.Protocol1_14
	feet := a.eye.Sub(geom.V(0, used, 0))
	b.addEye(a.eye)
	for _, h := range heights {
		if h != used {
			b.addEye(feet.Add(geom.V(0, h, 0)))
		}
	}
	if keepCrawl {
		b.addCrawlEye(feet.Add(geom.V(0, crawl, 0)))
	}
	if a.hasPrev {
		for _, h := range heights {
			for _, e := range eyePath(a.prevFeet, feet, h) {
				b.addEye(e)
			}
		}
		if keepCrawl {
			for _, e := range eyePath(a.prevFeet, feet, crawl) {
				b.addCrawlEye(e)
			}
		}
	}
	r1 := a.rot
	if next != nil && next.TimeUs-a.atUs <= blockActionWaitUs {
		if next.HasPos {
			for _, h := range heights {
				b.addEye(next.Pos.Add(geom.V(0, h, 0)))
			}
			if keepCrawl {
				b.addCrawlEye(next.Pos.Add(geom.V(0, crawl, 0)))
			}
		}
		if next.HasRot && (!v.TickClient || next.Tick == a.tick) {
			r1 = next.Rot
		}
	}
	for _, r := range geom.RotationCandidates(a.rot, r1) {
		b.rot[b.nRot] = r
		b.nRot++
	}
	b.r1 = r1
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

// cursorEyes returns the eyes scaffold.cursor judges the cursor point p of a click on face from
// (CHECKS.md 2.12): the candidates, plus the crawling eyes when face is the bottom face and p lies below
// every candidate (by more than the eye uncertainty u). A bottom face is only seen from below it, and
// the only vanilla eye below the standing and sneaking ones is the 0.4 eye of a crawl or swim the
// plugin does not flag (the pose-only crawl in a one-block gap).
func (b *blockCandidates) cursorEyes(face pb.Face, p geom.Vec3, u float64) []geom.Vec3 {
	eyes := b.eyes()
	if face != pb.Face_FACE_DOWN || b.nCrawl == 0 {
		return eyes
	}
	for _, e := range eyes {
		if e.Y-u < p.Y {
			return eyes // this eye may lie below the face
		}
	}
	return append(slices.Clip(eyes), b.crawl[:b.nCrawl]...)
}

// minAngleTo is the smallest angle (degrees) between a candidate look vector and the direction from an
// eye of eyes to p.
func (b *blockCandidates) minAngleTo(eyes []geom.Vec3, p geom.Vec3) float64 {
	best := math.Inf(1)
	for _, e := range eyes {
		for _, r := range b.rots() {
			if a, ok := geom.AngleBetween(r.Look(), p.Sub(e)); ok && a < best {
				best = a
			}
		}
	}
	return best
}

// rayAngle is the smallest angle (degrees) between a candidate look vector and the direction from its eye
// to a point of box: 0 when some candidate ray points into the box, at any distance. For a ray that misses
// the box the nearest direction lies on one of the box's 12 edges (from any other point, a move within the
// box or within its face turns the direction towards the look vector), and each edge is minimised exactly.
func (b *blockCandidates) rayAngle(box geom.AABB) float64 {
	edges := boxEdges(box)
	best := -1.0 // the largest cosine
	for _, e := range b.eyes() {
		for _, r := range b.rots() {
			look := r.Look()
			if _, hit := box.IntersectRay(e, look, math.Inf(1)); hit {
				return 0
			}
			for _, ed := range edges {
				best = math.Max(best, maxCosOnSegment(look, ed[0].Sub(e), ed[1].Sub(ed[0])))
			}
		}
	}
	return math.Acos(math.Min(1, best)) * 180 / math.Pi
}

// maxCosOnSegment is the largest cosine between the unit vector d and a + s*w for s in [0, 1]. The cosine
// (al + be*s) / |a + s*w| has at most one interior extremum: its derivative is zero where the linear
// (be*aa - al*aw) + (be*aw - al*ww)*s is, with al = d.a, be = d.w, aa = a.a, aw = a.w and ww = w.w.
func maxCosOnSegment(d, a, w geom.Vec3) float64 {
	cos := func(s float64) float64 {
		v := a.Add(w.Scale(s))
		if n := v.Len(); n > 0 {
			return d.Dot(v) / n
		}
		return 1 // the eye lies on the edge
	}
	best := math.Max(cos(0), cos(1))
	al, be, aa, aw, ww := d.Dot(a), d.Dot(w), a.LenSq(), a.Dot(w), w.LenSq()
	if den := be*aw - al*ww; den != 0 {
		if s := (al*aw - be*aa) / den; s > 0 && s < 1 {
			best = math.Max(best, cos(s))
		}
	}
	return best
}

// boxEdges returns the 12 edges of box as pairs of corners.
func boxEdges(box geom.AABB) [12][2]geom.Vec3 {
	var corner [8]geom.Vec3
	for i := range corner {
		corner[i] = box.Min
		if i&1 != 0 {
			corner[i].X = box.Max.X
		}
		if i&2 != 0 {
			corner[i].Y = box.Max.Y
		}
		if i&4 != 0 {
			corner[i].Z = box.Max.Z
		}
	}
	var out [12][2]geom.Vec3
	n := 0
	for i := range corner {
		for _, bit := range [3]int{1, 2, 4} {
			if i&bit == 0 {
				out[n] = [2]geom.Vec3{corner[i], corner[i|bit]}
				n++
			}
		}
	}
	return out
}

// observation returns what every observation of the action shares (its candidates and context), when the
// digest records (counting the distinct candidates takes a few hundred comparisons).
func (b *blockCandidates) observation(c *Ctx, a *blockAction) Observation {
	if !c.observing() {
		return Observation{}
	}
	o := Observation{Pose: a.pose, EyePath: a.hasPrev, Eyes: b.distinctEyes(), Rotations: b.distinctRots(),
		Dist: a.eye.Dist(geom.BlockBox(a.block[0], a.block[1], a.block[2]).Center()), GapUs: a.gapUs, Face: a.face, Action: a.action}
	o.Turn, _ = geom.AngleBetween(a.rot.Look(), b.r1.Look()) // exactly 0 when R1 is R0
	if a.hasPrev {
		o.Step = math.Hypot(a.eye.X-a.prevFeet.X, a.eye.Z-a.prevFeet.Z) // the feet share the eye's x and z
	}
	return o
}

// candidateTolerance: candidates closer than this (blocks, degrees) count once in the digest. A still
// player's path and next-move eyes repeat the event eyes, and without R1 the five rotations are R0.
const candidateTolerance = 1e-6

func (b *blockCandidates) distinctEyes() int {
	n := 0
	for i, e := range b.eyes() {
		dup := false
		for _, p := range b.eye[:i] {
			dup = dup || e.DistSq(p) < candidateTolerance*candidateTolerance
		}
		if !dup {
			n++
		}
	}
	return n
}

func (b *blockCandidates) distinctRots() int {
	n := 0
	for i, r := range b.rots() {
		dup := false
		for _, p := range b.rot[:i] {
			dup = dup || math.Abs(r.Yaw-p.Yaw) < candidateTolerance && math.Abs(r.Pitch-p.Pitch) < candidateTolerance
		}
		if !dup {
			n++
		}
	}
	return n
}
