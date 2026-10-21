package checks_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// cfMouse turns a rotation like the vanilla client: per frame, counts * (s*0.6F + 0.2F)^3 * 8, then
// Entity.turn adds (float)delta * 0.15F to the float rotation.
type cfMouse struct {
	yaw, pitch float32
	f          float64
}

func cfNewMouse(s float64) *cfMouse {
	d := s*float64(float32(0.6)) + float64(float32(0.2))
	return &cfMouse{f: d * d * d * 8}
}

func (m *cfMouse) frame(dx, dy float64) {
	m.yaw += float32(dx*m.f) * 0.15
	m.pitch += float32(dy*m.f) * 0.15
	m.pitch = float32(math.Max(-90, math.Min(90, float64(m.pitch))))
}

func (m *cfMouse) rot() geom.Rotation {
	return geom.Rotation{Yaw: float64(m.yaw), Pitch: float64(m.pitch)}
}

// tick moves the mouse over 1-4 frames by integer counts (or, with fractional, by trackpad deltas).
func (m *cfMouse) tick(rng *rand.Rand, maxCounts int, fractional bool) {
	frames := 1 + rng.IntN(4)
	per := max(1, maxCounts/frames)
	for i := 0; i < frames; i++ {
		dx := float64(rng.IntN(2*per+1) - per)
		dy := float64(rng.IntN(per/2+1) - per/4)
		if fractional {
			dx, dy = dx*0.37+rng.Float64(), dy*0.37+rng.Float64()
		}
		m.frame(dx, dy)
	}
}

// gcdScenario: idle mouse movement (calibration), a fight with attacks every 5 ticks, then idle
// movement again. aimbot replaces the fight's rotations with smooth off-grid ones.
func gcdScenario(t *testing.T, s float64, maxCounts int, fractional, aimbot bool, seed uint64) (*cfWorld, checks.Detector) {
	rng := rand.New(rand.NewPCG(seed, 1))
	w := cfNewWorld(t, 769)
	m := cfNewMouse(s)
	m.yaw = 37.5
	idle := func(n int) {
		for i := 0; i < n; i++ {
			w.advance()
			m.tick(rng, maxCounts, fractional)
			w.move(geom.Vec3{}, m.rot())
		}
	}
	idle(200)
	for i := 0; i < 80; i++ {
		w.advance()
		if i%5 == 0 {
			w.attack()
			w.swing()
		}
		if aimbot {
			// Smoothed rotation toward a moving point: continuous, off the mouse grid.
			m.yaw += float32(2.3*math.Sin(float64(i)/3) + rng.Float64()*0.9)
			m.pitch = float32(math.Max(-60, math.Min(60, float64(m.pitch)+rng.Float64()*0.8-0.4)))
		} else {
			m.tick(rng, maxCounts, fractional)
		}
		w.move(geom.Vec3{}, m.rot())
	}
	idle(120)
	det := cfFactory(t, "aim").New()
	return w, det
}

func runDetector(t *testing.T, det checks.Detector, events []*pb.Event) []checks.Emission {
	t.Helper()
	v := checks.NewView()
	var out []checks.Emission
	c := checks.NewCtx(v, nil, func(e checks.Emission) { out = append(out, e) })
	for _, ev := range events {
		v.Apply(ev, 0)
		c.SetEvent(ev)
		det.Handle(c, ev)
	}
	return out
}

func TestGCDStep(t *testing.T) {
	if !cfNear(checks.GCDStep(0.5), 0.15) || !cfNear(checks.GCDStep(0), 0.0096) || !cfNear(checks.GCDStep(1), 0.6144) {
		t.Fatal("step formula")
	}
	cal := checks.GCDCalibration{Calibrated: true, Step: 0.15}
	if !cal.OnGrid(0.15*37, 100) || cal.OnGrid(0.15*37+0.03, 100) || !cal.OnGrid(-0.3, 10) {
		t.Fatal("on-grid rule")
	}
}

func TestGCDCalibrates(t *testing.T) {
	for _, s := range []float64{0.5, 0.25, 0.9} {
		rng := rand.New(rand.NewPCG(7, uint64(s*100)))
		m := cfNewMouse(s)
		m.yaw = 123.4
		var ds, as []float64
		for len(ds) < 300 {
			prev := m.rot()
			m.tick(rng, 40, false)
			cur := m.rot()
			for _, d := range [][2]float64{{geom.WrapDegrees(cur.Yaw - prev.Yaw), cur.Yaw}, {cur.Pitch - prev.Pitch, cur.Pitch}} {
				if d[0] != 0 && math.Abs(d[0]) < 10 {
					ds, as = append(ds, d[0]), append(as, d[1])
				}
			}
		}
		cal := checks.CalibrateGCD(ds, as)
		want := checks.GCDStep(s)
		ratio := want / cal.Step
		if !cal.Calibrated || math.Abs(ratio-math.Round(ratio)) > 1e-3 {
			t.Fatalf("s=%v: calibration %+v, true step %v", s, cal, want)
		}
	}
}

func TestGCDLegitFight(t *testing.T) {
	w, det := gcdScenario(t, 0.5, 40, false, false, 1)
	es := runDetector(t, det, w.events)
	if n := checktest.Count(es, checks.AimGCD); n != 0 {
		t.Fatalf("legit fight flagged: %+v", es)
	}
	if cal := det.(checks.GCDSource).GCD(); !cal.Calibrated {
		t.Fatalf("legit mouse did not calibrate: %+v", cal)
	}
}

// Very low sensitivity: tiny steps, many counts per delta. On the grid either way, never flagged.
func TestGCDLowSensitivityLegit(t *testing.T) {
	w, det := gcdScenario(t, 0.02, 300, false, false, 2)
	if es := runDetector(t, det, w.events); checktest.Count(es, checks.AimGCD) != 0 {
		t.Fatalf("low sensitivity flagged: %+v", es)
	}
}

// A trackpad (fractional deltas) never calibrates, so even off-grid combat rotations are not flagged.
func TestGCDTrackpadNeverCalibrates(t *testing.T) {
	w, det := gcdScenario(t, 0.5, 40, true, true, 3)
	es := runDetector(t, det, w.events)
	if checktest.Count(es, checks.AimGCD) != 0 {
		t.Fatalf("trackpad flagged: %+v", es)
	}
	if det.(checks.GCDSource).GCD().Calibrated {
		t.Fatal("trackpad calibrated")
	}
}

func TestGCDCheat(t *testing.T) {
	w, det := gcdScenario(t, 0.5, 40, false, true, 4)
	es := runDetector(t, det, w.events)
	if n := checktest.Count(es, checks.AimGCD); n != 1 {
		t.Fatalf("aimbot fight: %d emissions, want 1: %+v", n, es)
	}
	if e := es[0]; e.Points != 4 || e.Confidence != 0.75 || e.Data["offgrid_share"] <= 0.4 || e.Data["post_share"] < 0.9 {
		t.Fatalf("emission %+v", e)
	}
}

// A changed sensitivity after the fight (idle rotations off the old grid too) recalibrates instead
// of flagging.
func TestGCDSensitivityChangeRecalibrates(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	w := cfNewWorld(t, 769)
	m := cfNewMouse(0.5)
	for i := 0; i < 200; i++ {
		w.advance()
		m.tick(rng, 40, false)
		w.move(geom.Vec3{}, m.rot())
	}
	m2 := cfNewMouse(0.83)
	m2.yaw, m2.pitch = m.yaw, m.pitch
	for i := 0; i < 80; i++ {
		w.advance()
		if i%5 == 0 {
			w.attack()
		}
		m2.tick(rng, 40, false)
		w.move(geom.Vec3{}, m2.rot())
	}
	for i := 0; i < 120; i++ {
		w.advance()
		m2.tick(rng, 40, false)
		w.move(geom.Vec3{}, m2.rot())
	}
	if n := checktest.Count(cfRun(t, "aim", w), checks.AimGCD); n != 0 {
		t.Fatalf("sensitivity change flagged %d times", n)
	}
}

// snapFight: attacks every 6 ticks; snap rotates 50 degrees out in the attack tick and 45 back in
// the next one.
func snapFight(t *testing.T, flags ...pb.MoveFlag) *cfWorld {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 6; i++ {
		w.advance()
		w.attack()
		w.swing()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 50}, flags...)
		w.advance()
		w.move(geom.Vec3{}, geom.Rotation{Yaw: 5}, flags...)
		w.idle(4)
		w.move(geom.Vec3{}, geom.Rotation{})
	}
	w.idle(5)
	return w
}

func TestSnapCheat(t *testing.T) {
	es := cfRun(t, "aim", snapFight(t))
	if n := checktest.Count(es, checks.AimSnap); n != 4 {
		t.Fatalf("snap emissions = %d, want 4 (from the third snap): %+v", n, es)
	}
	if es[0].Points != 5 || es[0].Confidence != 0.8 {
		t.Fatalf("scoring %+v", es[0])
	}
}

func TestSnapServerRotationLegit(t *testing.T) {
	if n := checktest.Count(cfRun(t, "aim", snapFight(t, pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)), checks.AimSnap); n != 0 {
		t.Fatalf("server-forced rotations flagged %d times", n)
	}
}

func TestSnapLegitFlick(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	yaw := 0.0
	for i := 0; i < 10; i++ {
		w.advance()
		w.attack()
		w.swing()
		yaw += 45 // a flick, no snap back
		w.move(geom.Vec3{}, geom.Rotation{Yaw: yaw})
		for k := 0; k < 4; k++ {
			w.advance()
			yaw -= 8 // slow correction
			w.move(geom.Vec3{}, geom.Rotation{Yaw: yaw})
		}
	}
	w.idle(5)
	if n := checktest.Count(cfRun(t, "aim", w), checks.AimSnap); n != 0 {
		t.Fatalf("flick shots flagged %d times", n)
	}
}

// precisionFight: a strafing target, the attacker's same-tick rotation (R1) aimed at the target box
// centre plus noise degrees.
func precisionFight(t *testing.T, noise func(i int) float64) *cfWorld {
	w := cfNewWorld(t, 769)
	w.ping = 40
	w.idle(30)
	for i := 0; i < 240; i++ {
		strafingTarget(w, i)
		w.advance()
		aim := w.lookAt(w.targetCentre())
		aim.Yaw += noise(i)
		aim.Pitch += noise(i+1) / 2
		if i%6 == 0 {
			w.attack()
			w.swing()
		}
		w.move(geom.Vec3{}, aim)
	}
	w.idle(5)
	return w
}

func TestPrecisionCheat(t *testing.T) {
	es := cfRun(t, "aim", precisionFight(t, func(int) float64 { return 0 }))
	if n := checktest.Count(es, checks.AimPrecision); n != 1 {
		t.Fatalf("precision emissions = %d, want 1 (at most one per 60 s): %+v", n, es)
	}
	if e := es[0]; e.Points != 4 || e.Confidence != 0.7 || e.Data["mean"] > 0.1 {
		t.Fatalf("emission %+v", e)
	}
}

func TestPrecisionLegit(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	es := cfRun(t, "aim", precisionFight(t, func(int) float64 { return rng.NormFloat64() * 3 }))
	if n := checktest.Count(es, checks.AimPrecision); n != 0 {
		t.Fatalf("human aim flagged %d times: %+v", n, es)
	}
}

func TestPrecisionStationaryTargetNotEligible(t *testing.T) {
	w := cfNewWorld(t, 769)
	w.idle(30)
	for i := 0; i < 200; i++ {
		w.advance()
		if i%6 == 0 {
			w.attack()
		}
		w.move(geom.Vec3{}, w.lookAt(w.targetCentre()))
	}
	if n := checktest.Count(cfRun(t, "aim", w), checks.AimPrecision); n != 0 {
		t.Fatalf("stationary target judged: %d", n)
	}
}
