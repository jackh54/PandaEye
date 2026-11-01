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
