package checks_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// digestRecorder is a DigestSink that keeps everything it is given.
type digestRecorder struct {
	obs     []checks.Observation
	emitted []string
	brands  []checks.BrandFamily // view brand family at each observation
}

func (d *digestRecorder) Observe(v *checks.View, o checks.Observation) {
	d.obs = append(d.obs, o)
	d.brands = append(d.brands, v.BrandFamily)
}

func (d *digestRecorder) Emitted(_ *checks.View, check string) { d.emitted = append(d.emitted, check) }

func (d *digestRecorder) of(check string) []checks.Observation {
	var out []checks.Observation
	for _, o := range d.obs {
		if o.Check == check {
			out = append(out, o)
		}
	}
	return out
}

func (r *rig) runDigest(factory string) ([]checks.Emission, *digestRecorder) {
	r.tb.Helper()
	d := &digestRecorder{}
	return checktest.RunDigest(r.tb, rigFactory(r.tb, factory), r.events, checks.DefaultConfig{}, d), d
}

func TestBrandFamily(t *testing.T) {
	for brand, want := range map[string]string{
		"":                         "unknown",
		"  ":                       "unknown",
		"vanilla":                  "vanilla",
		"Vanilla":                  "vanilla",
		"fabric":                   "fabric",
		"forge":                    "forge",
		"neoforge":                 "forge",
		"fml,forge":                "forge",
		"fabric (connector) forge": "forge",
		"lunarclient:v2.18.3-2451": "lunar",
		"Badlion":                  "badlion",
		"Feather Fabric":           "feather",
		"LabyMod":                  "labymod",
		"quilt":                    "other",
		"optifine":                 "other",
	} {
		if got := checks.BrandFamilyOf(brand).String(); got != want {
			t.Errorf("BrandFamilyOf(%q) = %s, want %s", brand, got, want)
		}
	}
	if checks.BrandFamily(200).String() != "other" {
		t.Error("an out-of-range family prints as other")
	}
}

// The brand reaches the view from the join and from the later update that carries it; an update
// without a brand keeps it, a new session starts without one.
func TestViewBrandFromJoinAndUpdate(t *testing.T) {
	r := newRig(t, rigProtocol)
	v := checks.NewView()
	join := r.events[0]
	join.GetJoin().Brand = ""
	v.Apply(join, 0)
	if v.Brand != "" || v.BrandFamily != checks.BrandUnknown {
		t.Fatalf("join without brand: %q %v", v.Brand, v.BrandFamily)
	}
	update := func(brand string) *pb.Event {
		return &pb.Event{Player: r.p.UUID, TimeUs: r.p.TimeUs, Kind: &pb.Event_Join{Join: &pb.PlayerJoin{Update: true, Brand: brand}}}
	}
	v.Apply(update("Feather Fabric 1.21"), 0)
	if v.Brand != "Feather Fabric 1.21" || v.BrandFamily != checks.BrandFeather {
		t.Fatalf("brand update: %q %v", v.Brand, v.BrandFamily)
	}
	v.Apply(update(""), 0) // a periodic re-announcement without the brand
	if v.BrandFamily != checks.BrandFeather {
		t.Fatalf("an update without brand must keep it: %v", v.BrandFamily)
	}
	v.Apply(join, 0)
	if v.Brand != "" || v.BrandFamily != checks.BrandUnknown {
		t.Fatalf("a new session must not keep the old brand: %q %v", v.Brand, v.BrandFamily)
	}
}

func TestDigestEdges(t *testing.T) {
	for _, id := range []string{"scaffold.face", "scaffold.ray", "scaffold.cursor", "scaffold.reach", "dig.ray"} {
		edges := checks.DigestEdges(id)
		if len(edges) == 0 || len(edges)+1 > checks.DigestBuckets || !slices.IsSorted(edges) || len(slices.Compact(slices.Clone(edges))) != len(edges) {
			t.Errorf("%s: edges %v", id, edges)
		}
	}
	if checks.DigestEdges("reach.distance") != nil {
		t.Error("checks without observations have no histogram")
	}
	angle := checks.DigestEdges("scaffold.cursor")
	for _, c := range []struct {
		v    float64
		want int
	}{{-1, 0}, {0, 0}, {1e-9, 1}, {0.25, 1}, {2.99, 7}, {3, 7}, {3.0001, 8}, {90, 16}, {90.5, 17}, {math.Inf(1), 17}} {
		if got := checks.DigestBucket(angle, c.v); got != c.want {
			t.Errorf("angle %v: bucket %d, want %d", c.v, got, c.want)
		}
	}
	margin := checks.DigestEdges("scaffold.face")
	if b := checks.DigestBucket(margin, 0); margin[b] != 0 {
		t.Error("a zero margin (not visible) belongs to the bucket ending at 0")
	}
	if b := checks.DigestBucket(margin, 1e-9); margin[b-1] != 0 {
		t.Error("a positive margin (visible) belongs to the bucket after 0")
	}
}

// Every judged placement is observed, legit or not; failures are the per-event misses, emissions the
// signals that actually went out after the "k of the last n" rule.
func TestDigestScaffoldObservations(t *testing.T) {
	r := newRig(t, rigProtocol)
	bridge(r, 10, nil)
	bridge(r, 12, func(geom.Rotation) geom.Rotation { return geom.Rotation{Yaw: 0, Pitch: 0} }) // looks south, places north
	es, d := r.runDigest("scaffold")
	for _, c := range []struct {
		check    string
		failures int
	}{{"scaffold.face", 0}, {"scaffold.ray", 12}, {"scaffold.cursor", 12}, {"scaffold.reach", 0}} {
		obs := d.of(c.check)
		failed, emitted := 0, 0
		for i, o := range obs {
			if o.Failed {
				failed++
			}
			if o.Emitted {
				emitted++
			}
			// A still sneaking player: the event eye and the standing one (the path and the next move
			// repeat them); the same tick's movement packet keeps R0.
			if o.Pose != geom.PoseSneaking || o.Eyes != 2 || o.Rotations != 1 || !o.EyePath {
				t.Errorf("%s #%d: %+v", c.check, i, o)
			}
		}
		if len(obs) != 22 || failed != c.failures || emitted != checktest.Count(es, c.check) {
			t.Errorf("%s: %d observations, %d failed, %d emitted; want 22, %d, %d", c.check, len(obs), failed, emitted,
				c.failures, checktest.Count(es, c.check))
		}
	}
	expectCount(t, es, "scaffold.ray", 4)
	for _, o := range d.of("scaffold.ray") {
		if o.Failed != (o.Value > 45) || !o.Failed && o.Value != 0 {
			t.Errorf("ray value %+v: 0 for a hit, the angle to the cube for a miss", o)
		}
	}
	for _, o := range d.of("scaffold.cursor") {
		if o.Failed != (o.Value > 45) || !o.Failed && o.Value > 0.25 {
			t.Errorf("cursor value %+v", o)
		}
	}
	for _, o := range d.of("scaffold.face") {
		if math.Abs(o.Value-(0.75+0.0302)) > 1e-6 { // the eye stands 0.25 into the block, u = 0.03 + 0.0002
			t.Errorf("face margin %+v", o)
		}
	}
	for _, o := range d.of("scaffold.reach") {
		if o.Value >= -3 || o.Value < -4.5 {
			t.Errorf("reach excess %+v", o)
		}
	}
	if len(d.emitted) != 0 {
		t.Fatalf("world-interaction emissions are counted in their observations: %v", d.emitted)
	}
	for _, b := range d.brands {
		if b != checks.BrandVanilla {
			t.Fatalf("view brand %v at an observation", b)
		}
	}
}

// R1 of the same tick and the eyes of a moving player's path are distinct candidates.
func TestDigestCandidatesWhileTurningAndMoving(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for i := range 6 {
		r.step(geom.V(0.2, 0, 0))
		b := [3]int32{int32(1 + i%3), 64, 3}
		eye := r.eyeOf(false)
		target := geom.V(float64(b[0])+0.5, 64.5, 3)
		r1 := aim(eye, target)
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: target.Sub(geom.V(float64(b[0]), 64, 3)), eye: eye,
			rot: geom.Rotation{Yaw: r1.Yaw + 30, Pitch: r1.Pitch}})
		r.tick(r.p.Pos, r1)
	}
	r.step(geom.V(0, 0, 0))
	es, d := r.runDigest("scaffold")
	if len(es) != 0 {
		t.Fatalf("fixture: legit placements flagged: %+v", es)
	}
	obs := d.of("scaffold.ray")
	if len(obs) != 6 {
		t.Fatalf("%d ray observations", len(obs))
	}
	for _, o := range obs {
		// The event eye and its sneaking alternative, 8 path points at both heights; the next move
		// stands still (repeats the event eyes). R0, R1 and three interpolations.
		if o.Eyes != 18 || o.Rotations != 5 || !o.EyePath || o.Pose != geom.PoseStanding {
			t.Errorf("candidates %+v", o)
		}
	}
}

func TestDigestDigObservations(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	for x := int32(-2); x <= 2; x++ {
		mine(r, [3]int32{x, 64, 2}, pb.Face_FACE_NORTH, 3, true, nil)
	}
	away := geom.Rotation{Yaw: 180, Pitch: -30} // the blocks are south and level, the player looks north and up
	for x := int32(-2); x <= 2; x++ {
		mine(r, [3]int32{x, 64, 2}, pb.Face_FACE_NORTH, 3, true, &away)
	}
	r.step(geom.V(0, 0, 0))
	es, d := r.runDigest("dig")
	obs := d.of("dig.ray")
	failed, emitted := 0, 0
	for _, o := range obs {
		if o.Failed {
			failed++
			if o.Value < 90 {
				t.Errorf("a dig looking away: angle %v", o.Value)
			}
		} else if o.Value != 0 {
			t.Errorf("a dig in view: angle %v", o.Value)
		}
		if o.Emitted {
			emitted++
		}
	}
	// START and FINISH of every dig are judged.
	if len(obs) != 20 || failed != 10 || emitted != checktest.Count(es, "dig.ray") || emitted != 2 {
		t.Fatalf("%d observations, %d failed, %d emitted (%d emissions)", len(obs), failed, emitted, checktest.Count(es, "dig.ray"))
	}
}

// Checks without observations report each emission; a silent context (warm start) records nothing.
func TestDigestEmissionsAndSilence(t *testing.T) {
	r := newRig(t, rigProtocol)
	steps := []invStep{{window: -1}}
	for range 30 {
		steps = append(steps, invStep{window: 0, dyaw: 6})
	}
	playInv(r, steps)
	es, d := r.runDigest("inventory")
	if n := checktest.Count(es, "inventory.rotate"); n == 0 || len(d.emitted) != n || len(d.obs) != 0 {
		t.Fatalf("%d emissions, digest %v", n, d.emitted)
	}
	for _, id := range d.emitted {
		if id != "inventory.rotate" {
			t.Fatalf("emitted %q", id)
		}
	}

	b := newRig(t, rigProtocol)
	bridge(b, 12, func(geom.Rotation) geom.Rotation { return geom.Rotation{} })
	silent := &digestRecorder{}
	v := checks.NewView()
	ctx := checks.NewCtx(v, nil, func(checks.Emission) { t.Fatal("a silent context emits nothing") })
	ctx.Digest, ctx.Silent = silent, true
	det := rigFactory(t, "scaffold").New()
	for _, ev := range b.events {
		v.Apply(ev, 0)
		ctx.SetEvent(ev)
		det.Handle(ctx, ev)
	}
	if len(silent.obs) != 0 || len(silent.emitted) != 0 {
		t.Fatalf("silent context recorded %d observations", len(silent.obs))
	}
}

// Bucket boundaries of every context: dist, gap and speed include their lower bound, turn its upper one
// (only no turn is 0); the first action counts as a long gap, no eye path as its own speed bucket.
func TestDigestContextBuckets(t *testing.T) {
	names := map[string]string{
		"dist": "<1.5 1.5-3 3-4.5 >=4.5", "turn": "0 0-5 5-30 >30", "gap": "<60 60-250 250-1000 >=1000",
		"speed": "none <0.05 0.05-0.2 0.2-0.4 >=0.4", "face": "up down side other", "action": "start finish",
	}
	for d := range checks.NumDigestContexts {
		if b := d.Buckets(); strings.Join(b, " ") != names[d.String()] || len(b) > checks.DigestContextBuckets {
			t.Errorf("context %d %q: buckets %q", d, d, b)
		}
	}
	type tc struct {
		v    float64
		want int
	}
	check := func(d checks.DigestContext, set func(*checks.Observation, float64), cases ...tc) {
		t.Helper()
		for _, c := range cases {
			var o checks.Observation
			set(&o, c.v)
			if got := d.Bucket(&o); got != c.want || got >= len(d.Buckets()) {
				t.Errorf("%s %v: bucket %d, want %d", d, c.v, got, c.want)
			}
		}
	}
	check(checks.DigestDist, func(o *checks.Observation, v float64) { o.Dist = v },
		tc{0, 0}, tc{1.4999, 0}, tc{1.5, 1}, tc{2.9999, 1}, tc{3, 2}, tc{4.4999, 2}, tc{4.5, 3}, tc{60, 3})
	check(checks.DigestTurn, func(o *checks.Observation, v float64) { o.Turn = v },
		tc{0, 0}, tc{1e-9, 1}, tc{5, 1}, tc{5.0001, 2}, tc{30, 2}, tc{30.0001, 3}, tc{180, 3})
	check(checks.DigestGap, func(o *checks.Observation, v float64) { o.GapUs = int64(v) },
		tc{-1, 3}, tc{0, 0}, tc{59_999, 0}, tc{60_000, 1}, tc{249_999, 1}, tc{250_000, 2}, tc{999_999, 2}, tc{1_000_000, 3}, tc{3.6e9, 3})
	check(checks.DigestSpeed, func(o *checks.Observation, v float64) { o.Step = v }, // no eye path, whatever the step
		tc{0, 0}, tc{0.3, 0})
	check(checks.DigestSpeed, func(o *checks.Observation, v float64) { o.EyePath, o.Step = true, v },
		tc{0, 1}, tc{0.0499, 1}, tc{0.05, 2}, tc{0.1999, 2}, tc{0.2, 3}, tc{0.3999, 3}, tc{0.4, 4}, tc{3.9, 4})
	check(checks.DigestFace, func(o *checks.Observation, v float64) { o.Face = pb.Face(v) },
		tc{float64(pb.Face_FACE_UP), 0}, tc{float64(pb.Face_FACE_DOWN), 1}, tc{float64(pb.Face_FACE_NORTH), 2},
		tc{float64(pb.Face_FACE_SOUTH), 2}, tc{float64(pb.Face_FACE_WEST), 2}, tc{float64(pb.Face_FACE_EAST), 2},
		tc{float64(pb.Face_FACE_OTHER), 3}, tc{99, 3})
	check(checks.DigestAction, func(o *checks.Observation, v float64) { o.Action = pb.BlockDig_Action(v) },
		tc{float64(pb.BlockDig_ACTION_START), 0}, tc{float64(pb.BlockDig_ACTION_FINISH), 1})

	contexts := func(check string) (out []string) {
		for _, d := range checks.DigestContexts(check) {
			out = append(out, d.String())
		}
		return out
	}
	for _, id := range []string{"scaffold.face", "scaffold.ray", "scaffold.cursor", "scaffold.reach"} {
		if got := contexts(id); !slices.Equal(got, []string{"dist", "turn", "gap", "speed", "face"}) ||
			checks.DigestCursorOnly(id) != (id == "scaffold.cursor") {
			t.Errorf("%s: contexts %v, cursor only %v", id, got, checks.DigestCursorOnly(id))
		}
	}
	if got := contexts("dig.ray"); !slices.Equal(got, []string{"dist", "turn", "gap", "speed", "action"}) || checks.DigestCursorOnly("dig.ray") {
		t.Errorf("dig.ray: contexts %v", got)
	}
	if checks.DigestContexts("reach.distance") != nil || checks.DigestCursorOnly("reach.distance") {
		t.Error("checks without observations have no context")
	}
}

// Every observation of an action carries its context: the distance from the event eye to the block's
// centre, the turn from R0 to R1, the time since the session's previous judged placement (dig), the eye
// path's step, the face or the dig action, and for scaffold.cursor whether the ray hit.
func TestDigestActionContext(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	b := [3]int32{0, 64, 2}
	target := geom.V(0.5, 64.5, 2) // the centre of the north face; the block's centre is 0.5 further south
	middle := geom.V(0.5, 0.5, 0)
	type want struct {
		dist, turn, step float64
		gapUs            int64
		eyePath          bool
		cursorFailed     bool
		rayHit           bool
	}
	var wants []want
	click := func(cursor geom.Vec3, rot geom.Rotation, w want) {
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: cursor, eye: r.eyeOf(false), rot: rot})
		wants = append(wants, w)
	}
	still := math.Sqrt(1.12*1.12 + 2*2) // from the eye at 0.5, 65.62, 0.5
	moved := math.Sqrt(0.3*0.3 + 1.12*1.12 + 2*2)

	// The first placement, before an eye path; R1 of the same tick turns 10 degrees down.
	r0 := aim(r.eyeOf(false), target)
	click(middle, r0, want{dist: still, turn: 10, gapUs: -1, rayHit: true})
	r.tick(r.p.Pos, geom.Rotation{Yaw: r0.Yaw, Pitch: r0.Pitch + 10})
	// 100 ms later after a 0.3 step: aimed, then a burst in the same tick with the cursor in the corner.
	r.step(geom.V(0.3, 0, 0))
	r2 := aim(r.eyeOf(false), target)
	click(middle, r2, want{dist: moved, step: 0.3, gapUs: 100_000, eyePath: true, rayHit: true})
	click(geom.V(0.05, 0.95, 0), r2, want{dist: moved, step: 0.3, eyePath: true, cursorFailed: true, rayHit: true})
	r.tick(r.p.Pos, r2)
	// Looking away while standing still: the ray misses too.
	away := geom.Rotation{Yaw: r2.Yaw + 180, Pitch: r2.Pitch}
	r.tick(r.p.Pos, away)
	click(middle, away, want{dist: moved, gapUs: 100_000, eyePath: true, cursorFailed: true})
	r.tick(r.p.Pos, away)
	mine(r, b, pb.Face_FACE_NORTH, 2, true, nil) // START and FINISH 150 ms apart
	// A new session starts without a previous placement.
	r.add(r.p.Join(), r.p.State())
	r.tick(r.p.Pos, r2)
	click(middle, r2, want{dist: moved, gapUs: -1, rayHit: true})
	r.tick(r.p.Pos, r2)
	r.step(geom.V(0, 0, 0))

	_, d := r.runDigest("scaffold")
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-4 } // float32 rotations
	for _, id := range []string{"scaffold.face", "scaffold.ray", "scaffold.cursor", "scaffold.reach"} {
		obs := d.of(id)
		if len(obs) != len(wants) {
			t.Fatalf("%s: %d observations, want %d", id, len(obs), len(wants))
		}
		for i, o := range obs {
			w := wants[i]
			if !near(o.Dist, w.dist) || !near(o.Turn, w.turn) || !near(o.Step, w.step) || o.GapUs != w.gapUs ||
				o.EyePath != w.eyePath || o.Face != pb.Face_FACE_NORTH || o.RayHit != (id == "scaffold.cursor" && w.rayHit) {
				t.Errorf("%s #%d: %+v, want %+v", id, i, o, w)
			}
			if w.turn == 0 && o.Turn != 0 {
				t.Errorf("%s #%d: R1 = R0 is no turn at all: %v", id, i, o.Turn)
			}
			if id == "scaffold.cursor" && o.Failed != w.cursorFailed || id == "scaffold.ray" && o.Failed == w.rayHit {
				t.Errorf("%s #%d: failed %v (%+v)", id, i, o.Failed, w)
			}
		}
	}

	_, d = r.runDigest("dig")
	digs := d.of("dig.ray")
	if len(digs) != 2 || digs[0].Action != pb.BlockDig_ACTION_START || digs[0].GapUs != -1 ||
		digs[1].Action != pb.BlockDig_ACTION_FINISH || digs[1].GapUs != 150_000 {
		t.Fatalf("digs %+v", digs)
	}
	for _, o := range digs {
		if !near(o.Dist, moved) || o.Turn != 0 || o.Face != pb.Face_FACE_NORTH || o.Failed || o.RayHit {
			t.Errorf("dig %+v", o)
		}
	}
}

// The context follows the judgement: the gap counts from the previous judged placement (one whose next
// move carries a server-set rotation is not judged), and the turn is that of the R1 the checks used (a
// tick client's move of a later tick is no R1, however far it turned).
func TestDigestContextFollowsTheJudgement(t *testing.T) {
	r := newRig(t, rigProtocol)
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	b := [3]int32{0, 64, 2}
	rot := aim(r.eyeOf(false), geom.V(0.5, 64.5, 2))
	click := func() {
		r.place(blockClick{block: b, face: pb.Face_FACE_NORTH, cursor: geom.V(0.5, 0.5, 0), eye: r.eyeOf(false), rot: rot})
	}
	r.tick(r.p.Pos, rot)
	click() // judged, the first
	r.tick(r.p.Pos, rot)
	click() // 50 ms later, not judged
	r.flags = uint32(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
	r.tick(r.p.Pos, rot)
	r.flags = 0
	r.tick(r.p.Pos, rot)
	click() // 150 ms after the first, in a tick without a movement packet
	r.idle()
	r.tick(r.p.Pos, geom.Rotation{Yaw: rot.Yaw, Pitch: rot.Pitch + 20})

	_, d := r.runDigest("scaffold")
	obs := d.of("scaffold.ray")
	if len(obs) != 2 || obs[0].GapUs != -1 || obs[1].GapUs != 150_000 || obs[0].Turn != 0 || obs[1].Turn != 0 ||
		obs[0].Rotations != 1 || obs[1].Rotations != 1 {
		t.Fatalf("observations %+v", obs)
	}
}
