package geom

import (
	"math"
	"testing"
)

const eps = 1e-9

func near(a, b float64) bool { return math.Abs(a-b) < eps }

func nearVec(a, b Vec3) bool { return near(a.X, b.X) && near(a.Y, b.Y) && near(a.Z, b.Z) }

func TestVec3(t *testing.T) {
	a, b := V(1, 2, 3), V(4, 6, 3)
	if a.Dist(b) != 5 || a.DistSq(b) != 25 || a.Add(b) != V(5, 8, 6) || b.Sub(a) != V(3, 4, 0) {
		t.Fatal("basic ops")
	}
	if V(1, 0, 0).Cross(V(0, 1, 0)) != V(0, 0, 1) || a.Dot(b) != 25 || V(3, 9, 4).HorizontalLen() != 5 {
		t.Fatal("cross/dot")
	}
	if n, ok := V(0, 0, 2).Normalize(); !ok || n != V(0, 0, 1) {
		t.Fatal("normalize")
	}
	if _, ok := V(0, 0, 0).Normalize(); ok {
		t.Fatal("zero vector normalised")
	}
	if _, ok := V(math.NaN(), 0, 0).Normalize(); ok {
		t.Fatal("NaN normalised")
	}
	if V(math.Inf(1), 0, 0).IsFinite() || !a.IsFinite() || a.Lerp(b, 0.5) != V(2.5, 4, 3) {
		t.Fatal("finite/lerp")
	}
}

func TestBoxes(t *testing.T) {
	b := PlayerBox(V(0, 64, 0), 1)
	if !nearVec(b.Min, V(-0.3, 64, -0.3)) || !nearVec(b.Max, V(0.3, 65.8, 0.3)) {
		t.Fatalf("player box %+v", b)
	}
	if s := PlayerBox(V(0, 0, 0), 2).Size(); !nearVec(s, V(1.2, 3.6, 1.2)) {
		t.Fatalf("scaled box %+v", s)
	}
	if s := PlayerBox(V(0, 0, 0), math.NaN()).Size(); !nearVec(s, V(0.6, 1.8, 0.6)) {
		t.Fatal("NaN scale must fall back to 1")
	}
	e := b.Expand(0.1)
	if !nearVec(e.Min, V(-0.4, 63.9, -0.4)) || !nearVec(e.Max, V(0.4, 65.9, 0.4)) {
		t.Fatalf("expand %+v", e)
	}
	if s := b.Expand(-5).Size(); s != (Vec3{}) {
		t.Fatalf("over-shrunk box must collapse to its centre: %+v", s)
	}
	if !b.Contains(V(0.3, 65.8, -0.3)) || b.Contains(V(0.31, 65, 0)) {
		t.Fatal("contains (inclusive surface)")
	}
	if d := b.DistanceTo(V(0, 65, 0)); d != 0 {
		t.Fatalf("inside distance %v", d)
	}
	if d := b.DistanceTo(V(3.3, 65, 0)); !near(d, 3) {
		t.Fatalf("face distance %v", d)
	}
	if d := b.DistanceTo(V(3.3, 69.8, 0.3)); !near(d, 5) {
		t.Fatalf("edge distance %v", d)
	}
	if !b.Intersects(BlockBox(0, 65, 0)) || b.Intersects(BlockBox(1, 65, 0)) {
		t.Fatal("intersects")
	}
	if u := BlockBox(0, 0, 0).Union(BlockBox(2, 2, 2)); u.Min != V(0, 0, 0) || u.Max != V(3, 3, 3) {
		t.Fatal("union")
	}
	if Box(V(1, 1, 1), V(0, 0, 0)) != BlockBox(0, 0, 0) {
		t.Fatal("Box must order corners")
	}
	if BlockBox(0, 0, 0).Offset(V(1, 2, 3)) != BlockBox(1, 2, 3) || BlockBox(0, 0, 0).Center() != V(0.5, 0.5, 0.5) {
		t.Fatal("offset/center")
	}
}

func TestIntersectRay(t *testing.T) {
	box := BlockBox(0, 0, 0)
	inf := math.Inf(1)
	cases := []struct {
		name   string
		o, d   Vec3
		max    float64
		hit    bool
		tEntry float64
	}{
		{"straight on", V(-2, 0.5, 0.5), V(1, 0, 0), inf, true, 2},
		{"unnormalised dir", V(-2, 0.5, 0.5), V(10, 0, 0), inf, true, 2},
		{"pointing away", V(-2, 0.5, 0.5), V(-1, 0, 0), inf, false, 0},
		{"inside", V(0.5, 0.5, 0.5), V(0, 0, 1), inf, true, 0},
		{"inside, zero dir", V(0.5, 0.5, 0.5), V(0, 0, 0), inf, true, 0},
		{"on surface", V(0, 0.5, 0.5), V(-1, 0, 0), inf, true, 0},
		{"parallel outside slab", V(-2, 1.5, 0.5), V(1, 0, 0), inf, false, 0},
		{"parallel grazing edge", V(-2, 1, 0.5), V(1, 0, 0), inf, true, 2},
		{"parallel inside slab", V(-2, 0.2, 0.8), V(1, 0, 0), inf, true, 2},
		{"diagonal", V(-1, -1, 0.5), V(1, 1, 0), inf, true, math.Sqrt2},
		{"diagonal miss", V(-1, -1.01, 0.5), V(1, -1, 0), inf, false, 0},
		{"beyond max", V(-2, 0.5, 0.5), V(1, 0, 0), 1.99, false, 0},
		{"exactly max", V(-2, 0.5, 0.5), V(1, 0, 0), 2, true, 2},
		{"zero dir outside", V(-2, 0.5, 0.5), V(0, 0, 0), inf, false, 0},
		{"NaN origin", V(math.NaN(), 0.5, 0.5), V(1, 0, 0), inf, false, 0},
		{"NaN dir", V(-2, 0.5, 0.5), V(math.NaN(), 0, 0), inf, false, 0},
		{"Inf dir", V(-2, 0.5, 0.5), V(math.Inf(1), 0, 0), inf, false, 0},
		{"NaN max", V(-2, 0.5, 0.5), V(1, 0, 0), math.NaN(), false, 0},
		{"from above", V(0.5, 5, 0.5), V(0, -1, 0), inf, true, 4},
	}
	for _, c := range cases {
		tEntry, hit := box.IntersectRay(c.o, c.d, c.max)
		if hit != c.hit || (hit && !near(tEntry, c.tEntry)) {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", c.name, tEntry, hit, c.tEntry, c.hit)
		}
	}
	if _, hit := (AABB{Min: V(math.NaN(), 0, 0), Max: V(1, 1, 1)}).IntersectRay(V(-1, 0.5, 0.5), V(1, 0, 0), inf); hit {
		t.Error("NaN box hit")
	}
}

func TestLookVector(t *testing.T) {
	cases := []struct {
		yaw, pitch float64
		want       Vec3
	}{
		{0, 0, V(0, 0, 1)},    // south
		{90, 0, V(-1, 0, 0)},  // west
		{180, 0, V(0, 0, -1)}, // north
		{-90, 0, V(1, 0, 0)},  // east
		{270, 0, V(1, 0, 0)},  // unwrapped yaw
		{0, 90, V(0, -1, 0)},  // straight down
		{0, -90, V(0, 1, 0)},  // straight up
		{45, 0, V(-math.Sqrt2/2, 0, math.Sqrt2/2)},
	}
	for _, c := range cases {
		if got := LookVector(c.yaw, c.pitch); !nearVec(got, c.want) {
			t.Errorf("LookVector(%v, %v) = %+v, want %+v", c.yaw, c.pitch, got, c.want)
		}
		if l := LookVector(c.yaw, c.pitch).Len(); !near(l, 1) {
			t.Errorf("not unit: %v", l)
		}
	}
	for _, r := range []Rotation{{0, 0}, {37, -20}, {-170, 45}, {179.5, 89}} {
		got, ok := Angles(r.Look())
		if !ok || !nearVec(got.Look(), r.Look()) || got.Yaw < -180 || got.Yaw >= 180 {
			t.Errorf("Angles(Look(%+v)) = %+v", r, got)
		}
	}
	if _, ok := Angles(Vec3{}); ok {
		t.Error("Angles of zero vector")
	}
	if r, ok := Angles(V(0, -1, 0)); !ok || r.Pitch != 90 {
		t.Errorf("straight down: %+v", r)
	}
}

func TestAngleBetween(t *testing.T) {
	for _, c := range []struct {
		a, b Vec3
		want float64
	}{
		{V(1, 0, 0), V(1, 0, 0), 0},
		{V(1, 0, 0), V(0, 3, 0), 90},
		{V(1, 0, 0), V(-2, 0, 0), 180},
		{V(1, 0, 0), V(1, 1, 0), 45},
	} {
		got, ok := AngleBetween(c.a, c.b)
		if !ok || !near(got, c.want) {
			t.Errorf("AngleBetween(%v, %v) = %v", c.a, c.b, got)
		}
	}
	tiny, ok := AngleBetween(V(1, 0, 0), LookVector(-90+1e-6, 0))
	if !ok || math.Abs(tiny-1e-6) > 1e-9 {
		t.Errorf("tiny angle precision: %v", tiny)
	}
	for _, bad := range [][2]Vec3{{{}, V(1, 0, 0)}, {V(1, 0, 0), {}}, {V(math.NaN(), 0, 0), V(1, 0, 0)}, {V(1, 0, 0), V(0, math.Inf(-1), 0)}} {
		if _, ok := AngleBetween(bad[0], bad[1]); ok {
			t.Errorf("degenerate input accepted: %v", bad)
		}
	}
}

func TestYawInterpolation(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0, 0}, {180, -180}, {-180, -180}, {190, -170}, {-190, 170}, {720 + 45, 45}, {359.5, -0.5}} {
		if got := WrapDegrees(c.in); !near(got, c.want) {
			t.Errorf("WrapDegrees(%v) = %v", c.in, got)
		}
	}
	if !math.IsNaN(WrapDegrees(math.NaN())) || !math.IsNaN(WrapDegrees(math.Inf(1))) {
		t.Error("non-finite wrap")
	}
	if d := YawDelta(170, -170); !near(d, 20) {
		t.Errorf("YawDelta across seam = %v", d)
	}
	if d := YawDelta(-170, 170); !near(d, -20) {
		t.Errorf("YawDelta across seam backwards = %v", d)
	}
	if m := LerpYaw(170, -170, 0.5); !near(WrapDegrees(m), -180) {
		t.Errorf("midpoint across seam = %v", m)
	}
	if m := LerpYaw(10, 30, 0.25); !near(m, 15) {
		t.Errorf("lerp = %v", m)
	}
	if m := LerpYaw(3600, 3610, 0.5); !near(m, 3605) {
		t.Errorf("unwrapped yaw must stay unwrapped: %v", m)
	}
	c := RotationCandidates(Rotation{170, 0}, Rotation{-170, 20})
	if len(c) != 5 || c[0] != (Rotation{170, 0}) || c[4] != (Rotation{-170, 20}) || !near(c[2].Yaw, 180) || !near(c[2].Pitch, 10) || !near(c[1].Yaw, 175) {
		t.Errorf("candidates %+v", c)
	}
}

func TestEyeHeights(t *testing.T) {
	for _, c := range []struct {
		pose     Pose
		protocol int32
		scale    float64
		want     float64
	}{
		{PoseStanding, 769, 1, 1.62},
		{PoseSneaking, 769, 1, 1.27},
		{PoseSneaking, Protocol1_14, 1, 1.27},
		{PoseSneaking, Protocol1_14 - 1, 1, 1.54},
		{PoseSneaking, 340, 1, 1.54}, // 1.12.2
		{PoseSneaking, -1, 1, 1.27},  // unknown = current
		{PoseSwimming, 769, 1, 0.4},
		{PoseGliding, 47, 1, 0.4},
		{PoseCrawling, 769, 1, 0.4},
		{PoseStanding, 769, 0.5, 0.81},
		{PoseStanding, 769, 0, 1.62},
		{PoseStanding, 769, math.Inf(1), 1.62},
	} {
		if got := EyeHeight(c.pose, c.protocol, c.scale); !near(got, c.want) {
			t.Errorf("EyeHeight(%v, %v, %v) = %v, want %v", c.pose, c.protocol, c.scale, got, c.want)
		}
	}
	if Eye(V(1, 64, 1), PoseStanding, 769, 1) != V(1, 65.62, 1) {
		t.Error("Eye")
	}
	if EyeUncertainty(757) != 0.03 || EyeUncertainty(758) != 0.0002 || EyeUncertainty(-1) != 0.03 {
		t.Error("EyeUncertainty")
	}
}

func TestReachScenario(t *testing.T) {
	// An attacker eye at 2.95 blocks horizontal distance from a target's box surface is within the
	// 3.0 vanilla range; the box is expanded by the 0.1 margin first.
	target := PlayerBox(V(3.25, 64, 0), 1).Expand(0.1)
	eye := Eye(V(0, 64, 0), PoseStanding, 769, 1)
	if d := target.DistanceTo(eye); !near(d, 2.85) {
		t.Fatalf("distance %v", d)
	}
	look := LookVector(-90, 0) // east, towards +x
	tEntry, hit := target.IntersectRay(eye, look, 3.0+EyeUncertainty(769))
	if !hit || !near(tEntry, 2.85) {
		t.Fatalf("ray %v %v", tEntry, hit)
	}
}
