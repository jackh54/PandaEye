package checks

import (
	"fmt"
	"math"
	"sort"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// aim.gcd, aim.snap and aim.precision (CHECKS.md 2.6 - 2.8).

// Check ids of the aim family.
const (
	AimGCD       = "aim.gcd"
	AimSnap      = "aim.snap"
	AimPrecision = "aim.precision"
)

func init() {
	Register(Factory{Name: "aim", Checks: []string{AimGCD, AimSnap, AimPrecision}, New: func() Detector { return newAimDetector() }})
}

// GCDSource is implemented by the aim detector: the current aim.gcd mouse-step calibration of the
// player (read by the ML feature windows, ml/FEATURES.md features 29-31).
type GCDSource interface {
	GCD() GCDCalibration
}

type aimDetector struct {
	gcd  gcdState
	snap snapState
	prec precisionState
}

func newAimDetector() *aimDetector {
	return &aimDetector{
		gcd:  gcdState{deltas: NewRing[gcdDelta](gcdKeep)},
		snap: snapState{ticks: map[uint32]snapTick{}, recent: NewRing[bool](snapWindow)},
		prec: precisionState{angles: NewRing[float64](precisionWindow)},
	}
}

func (d *aimDetector) Handle(c *Ctx, ev *pb.Event) {
	d.prec.handle(c, ev)
	d.snap.handle(c, ev)
	d.gcd.handle(c, ev)
}

// GCD implements GCDSource.
func (d *aimDetector) GCD() GCDCalibration { return d.gcd.cal }

// ---------------------------------------------------------------------------------------------
// aim.gcd
// ---------------------------------------------------------------------------------------------

// Mouse rotations are integer multiples of step = (s*0.6 + 0.2)^3 * 8 * 0.15 degrees for sensitivity
// s. The check calibrates the step on idle rotations (no attack in the last 2 s) and flags combat
// rotations that leave the grid while the idle rotations after the fight are still on it.
const (
	gcdKeep            = 300 // calibration deltas kept
	gcdMinDeltas       = 80
	gcdRecalEvery      = 25 // new calibration deltas between recalibrations
	gcdCalibratedShare = 0.9
	gcdBigCounts       = 10  // a candidate step needs >= 20% of the deltas at >= 10 counts
	gcdBigShare        = 0.2 //
	gcdIdleMaxDelta    = 10.0
	gcdCombatMaxDelta  = 30.0
	gcdMaxPitch        = 89.0
	gcdIdleAfterUs     = 2_000_000
	gcdCombatWithinUs  = 1_000_000
	gcdMinCombat       = 30
	gcdOffGridShare    = 0.4
	gcdPostDeltas      = 20 // idle deltas after the fight that must still be on the grid
	gcdPostShare       = 0.9
	gcdViolationGapUs  = 30_000_000
	gcdTeleportPackets = 2
	gcdCoarseTol       = 0.15 // coarse search tolerance (fraction of the step): finds the basin
	gcdCoarseKeep      = 3    // coarse candidates refined by least squares
)

// GCDStep returns the rotation of one mouse count, in degrees, for sensitivity s in [0, 1].
func GCDStep(s float64) float64 {
	f := s*0.6 + 0.2
	return f * f * f * 8 * 0.15
}

// GCDCalibration is aim.gcd's mouse-step calibration.
type GCDCalibration struct {
	Calibrated bool
	Step       float64 // degrees per mouse count
	Sigma      float64 // standard error of Step (least squares)
	Share      float64 // on-grid share of the calibration deltas
	N          int     // calibration deltas used
}

// OnGrid applies aim.gcd's rule to a rotation delta d seen at the absolute angle (yaw or pitch as
// sent): |d - n*step| < 0.05*step + n*sigma + 2*ulp(|angle|) with n = round(|d| / step).
func (g GCDCalibration) OnGrid(d, angle float64) bool {
	if !(g.Step > 0) {
		return false
	}
	return gcdOnGrid(math.Abs(d), g.Step, g.Sigma, 0.05, 2*Float32ULP(angle))
}

func gcdOnGrid(d, step, sigma, tol, ulps float64) bool {
	n := math.Round(d / step)
	return math.Abs(d-n*step) < tol*step+n*sigma+ulps
}

// Float32ULP returns the spacing of float32 values at |x| (rotations travel as float32).
func Float32ULP(x float64) float64 {
	a := float32(math.Abs(x))
	if math.IsInf(float64(a), 0) || a != a {
		return math.Inf(1)
	}
	return float64(math.Nextafter32(a, float32(math.Inf(1))) - a)
}

type gcdDelta struct {
	d    float64 // absolute rotation delta, degrees
	ulps float64 // 2 * ulp(|angle|)
}

// CalibrateGCD fits the mouse step to rotation deltas (absolute values, degrees) seen at the given
// absolute angles: a coarse search over s in 0..1 (step 0.005) for candidate steps with >= 20% of the
// deltas at >= 10 counts, a least-squares refinement of the best candidates over n = round(d/step),
// and the on-grid share with the refined step and its standard error. Calibrated when >= 90% of
// >= 80 deltas are on the grid.
func CalibrateGCD(deltas, angles []float64) GCDCalibration {
	ds := make([]gcdDelta, len(deltas))
	for i, d := range deltas {
		a := 0.0
		if i < len(angles) {
			a = angles[i]
		}
		ds[i] = gcdDelta{d: math.Abs(d), ulps: 2 * Float32ULP(a)}
	}
	return calibrateGCD(ds)
}

func calibrateGCD(ds []gcdDelta) GCDCalibration {
	n := len(ds)
	if n == 0 {
		return GCDCalibration{}
	}
	type cand struct {
		step  float64
		score int
	}
	var top []cand
	for i := 0; i <= 200; i++ {
		step := GCDStep(float64(i) * 0.005)
		big, on := 0, 0
		for _, d := range ds {
			if d.d >= gcdBigCounts*step {
				big++
			}
			if gcdOnGrid(d.d, step, 0, gcdCoarseTol, d.ulps) && d.d >= step/2 {
				on++
			}
		}
		if float64(big) < gcdBigShare*float64(n) || on == 0 {
			continue
		}
		top = append(top, cand{step, on})
		sort.Slice(top, func(a, b int) bool {
			if top[a].score != top[b].score {
				return top[a].score > top[b].score
			}
			return top[a].step > top[b].step
		})
		if len(top) > gcdCoarseKeep {
			top = top[:gcdCoarseKeep]
		}
	}
	best := GCDCalibration{N: n}
	for _, c := range top {
		step, sigma := gcdRefine(ds, c.step)
		if !(step > 0) {
			continue
		}
		on := 0
		for _, d := range ds {
			if gcdOnGrid(d.d, step, sigma, 0.05, d.ulps) && d.d >= step/2 {
				on++
			}
		}
		share := float64(on) / float64(n)
		if share > best.Share || (share == best.Share && step > best.Step) {
			best.Step, best.Sigma, best.Share = step, sigma, share
		}
	}
	best.Calibrated = n >= gcdMinDeltas && best.Share >= gcdCalibratedShare
	return best
}

// gcdRefine fits step by least squares over the deltas within a quarter step of the grid
// (fixed-point iteration on n = round(d/step)) and returns it with its standard error.
func gcdRefine(ds []gcdDelta, step float64) (float64, float64) {
	var sumNN, rss float64
	m := 0
	for iter := 0; iter < 6; iter++ {
		var sumND float64
		sumNN, m = 0, 0
		for _, d := range ds {
			k := math.Round(d.d / step)
			if k < 1 || math.Abs(d.d-k*step) > 0.25*step {
				continue
			}
			sumND += k * d.d
			sumNN += k * k
			m++
		}
		if sumNN == 0 {
			return 0, 0
		}
		next := sumND / sumNN
		converged := math.Abs(next-step) <= 1e-12*step
		step = next
		if converged {
			break
		}
	}
	rss, m = 0, 0
	sumNN = 0
	for _, d := range ds {
		k := math.Round(d.d / step)
		if k < 1 || math.Abs(d.d-k*step) > 0.25*step {
			continue
		}
		r := d.d - k*step
		rss += r * r
		sumNN += k * k
		m++
	}
	if m < 2 || sumNN == 0 {
		return step, 0
	}
	return step, math.Sqrt(rss/float64(m-1)) / math.Sqrt(sumNN)
}

type gcdState struct {
	deltas        Ring[gcdDelta] // calibration (idle) deltas
	sinceCal      int
	cal           GCDCalibration
	afterTeleport int // movement packets still skipped after a teleport

	// The current fight: combat deltas judged against the calibration in force at each one.
	combatN, combatOff int
	// After a fight that left the grid: idle deltas must still be on it for a violation.
	verifying     bool
	postN, postOn int
	lastViolation int64 // normalised time; 0 = never
}

func (g *gcdState) handle(c *Ctx, ev *pb.Event) {
	v := c.View
	switch ev.GetKind().(type) {
	case *pb.Event_Teleport:
		g.afterTeleport = gcdTeleportPackets
		return
	case *pb.Event_Move:
	default:
		return
	}
	m, _ := v.Moves.Last()
	if g.afterTeleport > 0 {
		g.afterTeleport--
		return
	}
	prev, ok := v.Moves.FromEnd(1)
	if !ok || !m.HasRot || m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) || m.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE) ||
		m.Has(pb.MoveFlag_MOVE_FLAG_SCOPING) || v.InVehicle {
		return
	}
	since := v.Since(v.LastAttackUs)
	idle, combat := since >= gcdIdleAfterUs, since <= gcdCombatWithinUs
	if idle {
		g.endFight()
	}
	if (!idle && !combat) || math.Abs(m.Rot.Pitch) >= gcdMaxPitch {
		return
	}
	axes := [2][2]float64{
		{math.Abs(geom.WrapDegrees(m.Rot.Yaw - prev.Rot.Yaw)), math.Max(math.Abs(m.Rot.Yaw), math.Abs(prev.Rot.Yaw))},
		{math.Abs(m.Rot.Pitch - prev.Rot.Pitch), math.Max(math.Abs(m.Rot.Pitch), math.Abs(prev.Rot.Pitch))},
	}
	for _, ax := range axes {
		d, angle := ax[0], ax[1]
		if d == 0 || math.IsNaN(d) {
			continue
		}
		switch {
		case idle && d < gcdIdleMaxDelta:
			g.deltas.Push(gcdDelta{d: d, ulps: 2 * Float32ULP(angle)})
			g.sinceCal++
			if g.verifying {
				g.postN++
				if g.cal.OnGrid(d, angle) {
					g.postOn++
				}
			}
		case combat && d < gcdCombatMaxDelta && g.cal.Calibrated:
			g.combatN++
			if !g.cal.OnGrid(d, angle) {
				g.combatOff++
			}
		}
	}
	if idle {
		g.afterIdle(c)
	}
}

// endFight runs at the first idle sample after a fight: a fight that left the grid is verified
// against the following idle deltas, any other fight is forgotten.
func (g *gcdState) endFight() {
	if g.verifying || g.combatN == 0 {
		return
	}
	if g.cal.Calibrated && g.combatN >= gcdMinCombat && float64(g.combatOff) > gcdOffGridShare*float64(g.combatN) {
		g.verifying, g.postN, g.postOn = true, 0, 0
		return
	}
	g.combatN, g.combatOff = 0, 0
}

func (g *gcdState) afterIdle(c *Ctx) {
	if g.verifying && g.postN >= gcdPostDeltas {
		off := float64(g.combatOff) / float64(g.combatN)
		post := float64(g.postOn) / float64(g.postN)
		now := c.View.NowUs
		minShare := c.Param(AimGCD, "offgrid_share", gcdOffGridShare)
		switch {
		case post < gcdPostShare:
			// The idle rotations left the grid too: the sensitivity changed. Recalibrate from scratch.
			g.cal, g.sinceCal = GCDCalibration{}, 0
			g.deltas.Reset()
		case off > minShare && float64(g.combatN) >= c.Param(AimGCD, "min_combat", gcdMinCombat) &&
			(g.lastViolation == 0 || now-g.lastViolation >= gcdViolationGapUs):
			g.lastViolation = now
			c.Emit(Emission{
				Check: AimGCD, Points: 4, Confidence: 0.75,
				Info: fmt.Sprintf("%.0f%% of %d combat rotations off the mouse grid (step %.5f), %.0f%% on it after the fight",
					100*off, g.combatN, g.cal.Step, 100*post),
				Data: map[string]float64{"offgrid_share": off, "combat_deltas": float64(g.combatN), "step": g.cal.Step,
					"post_share": post, "calibration_share": g.cal.Share},
			})
		}
		g.verifying, g.combatN, g.combatOff, g.postN, g.postOn = false, 0, 0, 0, 0
	}
	if !g.verifying && g.sinceCal >= gcdRecalEvery && g.deltas.Len() >= gcdMinDeltas {
		g.cal = calibrateGCD(g.deltas.Slice())
		g.sinceCal = 0
	}
}

// ---------------------------------------------------------------------------------------------
// aim.snap
// ---------------------------------------------------------------------------------------------

// A snap is |dyaw| >= 40 in the attack tick or the tick before, followed within 2 ticks by
// |dyaw| >= 30 in the opposite direction, none of them server-set. Tick clients only (the definition
// is per client tick). Violation when >= 3 of the last 20 attack ticks had one.
const (
	snapWindow   = 20
	snapSnaps    = 3
	snapOut      = 40.0
	snapBack     = 30.0
	snapAfter    = 2  // ticks within which the snap back must follow
	snapGiveUp   = 10 // ticks after which an attack tick is decided without further moves
	snapTicksMax = 64
)

type snapTick struct {
	dyaw   float64
	known  bool // full-rate telemetry covered the tick
	server bool
}

type snapAttack struct {
	tick     uint32
	pluginUs uint64
	skipped  bool
}

type snapState struct {
	ticks    map[uint32]snapTick
	lastTick uint32 // tick of the newest move
	lastYaw  float64
	haveYaw  bool
	pending  []snapAttack
	recent   Ring[bool]
}

// SnapTicks records per-tick yaw changes of a tick client and decides aim.snap's snap for attack
// ticks. The ML features use it with the same definition.
type SnapTicks struct{ s snapState }

// NewSnapTicks returns an empty tracker.
func NewSnapTicks() *SnapTicks {
	return &SnapTicks{s: snapState{ticks: map[uint32]snapTick{}}}
}

// ObserveMove records the newest move of v (call after View.Apply of a MoveSample).
func (t *SnapTicks) ObserveMove(v *View) { t.s.observeMove(v) }

// Snap decides whether attack tick T had a snap; decided is false while ticks up to T+2 may still
// change (the newest move is not beyond T+2).
func (t *SnapTicks) Snap(tick uint32) (snap, decided bool) {
	if t.s.lastTick <= tick+snapAfter {
		return false, false
	}
	return t.s.snapAt(tick), true
}

// Prune forgets ticks before tick.
func (t *SnapTicks) Prune(before uint32) { t.s.prune(before) }

func (s *snapState) observeMove(v *View) {
	m, ok := v.Moves.Last()
	if !ok || m.Tick == 0 {
		return
	}
	k := m.Tick
	server := m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
	switch {
	case s.haveYaw && k == s.lastTick:
		// A second movement packet in the same tick (teleport confirmation, server rotation).
		t := s.ticks[k]
		if m.HasRot {
			t.dyaw += geom.WrapDegrees(m.Rot.Yaw - s.lastYaw)
		}
		t.server = t.server || server
		s.ticks[k] = t
	case s.haveYaw && k > s.lastTick && m.Focus:
		for t := s.lastTick + 1; t < k && k-t < snapTicksMax; t++ {
			s.ticks[t] = snapTick{known: true} // no movement packet: the rotation did not change
		}
		t := snapTick{known: true, server: server}
		if m.HasRot {
			t.dyaw = geom.WrapDegrees(m.Rot.Yaw - s.lastYaw)
		}
		s.ticks[k] = t
	case k > s.lastTick || !s.haveYaw:
		s.ticks[k] = snapTick{} // down-sampled telemetry: the change may span several ticks
	default:
		return // out of order
	}
	s.lastTick, s.lastYaw, s.haveYaw = k, m.Rot.Yaw, true
	if len(s.ticks) > 4*snapTicksMax {
		s.prune(k - snapTicksMax)
	}
}

func (s *snapState) prune(before uint32) {
	for t := range s.ticks {
		if t < before {
			delete(s.ticks, t)
		}
	}
}

func (s *snapState) snapAt(tick uint32) bool {
	for _, k := range [...]uint32{tick - 1, tick} {
		out, ok := s.ticks[k]
		if !ok || !out.known || out.server || math.Abs(out.dyaw) < snapOut || k == 0 {
			continue
		}
		for j := k + 1; j <= k+snapAfter; j++ {
			back, ok := s.ticks[j]
			if ok && back.known && !back.server && math.Abs(back.dyaw) >= snapBack && math.Signbit(back.dyaw) != math.Signbit(out.dyaw) {
				return true
			}
		}
	}
	return false
}

func (s *snapState) handle(c *Ctx, ev *pb.Event) {
	v := c.View
	if !v.TickClient {
		return
	}
	switch ev.GetKind().(type) {
	case *pb.Event_Move:
		s.observeMove(v)
	case *pb.Event_Attack:
		if t := ev.GetTick(); t > 0 {
			a, _ := NewAttackObs(v, ev)
			skip := a.SkipReason(false) != ""
			if n := len(s.pending); n > 0 && s.pending[n-1].tick == t {
				s.pending[n-1].skipped = s.pending[n-1].skipped || skip
			} else {
				s.pending = append(s.pending, snapAttack{tick: t, pluginUs: ev.GetTimeUs(), skipped: skip})
			}
		}
	}
	_, quit := ev.GetKind().(*pb.Event_Quit)
	for len(s.pending) > 0 {
		p := s.pending[0]
		// Decided once a move beyond T+2 filled the ticks in between (or long after, or at the quit).
		if !quit && s.lastTick <= p.tick+snapAfter && ev.GetTick() <= p.tick+snapAfter+snapGiveUp {
			break
		}
		s.pending = s.pending[1:]
		if p.skipped {
			continue
		}
		snap := s.snapAt(p.tick)
		s.recent.Push(snap)
		if !snap {
			continue
		}
		n := cwCountTrue(&s.recent)
		if float64(n) < c.Param(AimSnap, "snaps", snapSnaps) {
			continue
		}
		c.Emit(Emission{
			Check: AimSnap, Points: 5, Confidence: 0.8, EventTimeUs: p.pluginUs,
			Info: fmt.Sprintf("snap and snap back around attack tick %d (%d of the last %d attack ticks)", p.tick, n, s.recent.Len()),
			Data: map[string]float64{"snaps": float64(n), "attack_ticks": float64(s.recent.Len()),
				"dyaw_prev": s.ticks[p.tick-1].dyaw, "dyaw": s.ticks[p.tick].dyaw, "dyaw_next": s.ticks[p.tick+1].dyaw},
		})
	}
	if len(s.pending) > 0 {
		s.prune(s.pending[0].tick - 2)
	} else if s.lastTick > snapTicksMax {
		s.prune(s.lastTick - snapTicksMax)
	}
}

// ---------------------------------------------------------------------------------------------
// aim.precision
// ---------------------------------------------------------------------------------------------

// Over the last 25 eligible attacks (PrecisionAngle): mean < 1.2 degrees and standard deviation
// < 0.6 -> violation, at most once per 60 s.
const (
	precisionWindow = 25
	precisionMean   = 1.2
	precisionSD     = 0.6
	precisionGapUs  = 60_000_000
)

type precisionState struct {
	pending       PendingR1[AttackObs]
	angles        Ring[float64]
	lastViolation int64
}

func (p *precisionState) handle(c *Ctx, ev *pb.Event) {
	v := c.View
	for _, e := range p.pending.Resolve(v, ev) {
		angle, ok := PrecisionAngle(e)
		if !ok {
			continue
		}
		p.angles.Push(angle)
		if p.angles.Len() < precisionWindow {
			continue
		}
		xs := p.angles.Slice()
		mean, sd := Mean(xs), PopStdDev(xs)
		if mean >= c.Param(AimPrecision, "max_mean", precisionMean) || sd >= c.Param(AimPrecision, "max_sd", precisionSD) {
			continue
		}
		if p.lastViolation != 0 && e.TimeUs-p.lastViolation < precisionGapUs {
			continue
		}
		p.lastViolation = e.TimeUs
		c.Emit(Emission{
			Check: AimPrecision, Points: 4, Confidence: 0.7, EventTimeUs: uint64(e.Item.PluginUs),
			Info: fmt.Sprintf("aim %.2f +- %.2f degrees from the target centre over %d attacks", mean, sd, len(xs)),
			Data: map[string]float64{"mean": mean, "sd": sd, "attacks": float64(len(xs))},
		})
	}
	if a, ok := NewAttackObs(v, ev); ok && !a.Stab {
		p.pending.Add(v, a, a.R0)
	}
}

// PopStdDev is the population standard deviation sqrt(sum((x-mean)^2)/n) (0 for no values).
func PopStdDev(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := Mean(xs)
	s := 0.0
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)))
}
