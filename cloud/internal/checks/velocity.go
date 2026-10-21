package checks

import (
	"fmt"
	"math"
	"math/bits"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// velocity.vertical and velocity.horizontal (CHECKS.md 2.9, 2.10).
//
// A knockback (VelocityApplied, source VELOCITY_PACKET) sent at T opens a window
// [T, T + rttHigh + 400 ms]. The client applies the velocity in the first tick after it received it, so
// the move that carries it arrives somewhere in that window; every consecutive position move in the window
// is a candidate for it. A candidate matches when its dy equals vy (or a jump reset, or a bump into a low
// ceiling). The window is judged when it ends: matched, ignored (>= 3 consecutive position moves and no
// match), or not judged (too few moves). A newer velocity replaces a pending one (an older one that already
// matched is judged as matched first), a teleport or an explosion cancels it.
//
// Horizontal: for matched velocities with |v_xz| >= 0.3, the matching move's horizontal displacement must
// reach h_min = |v_xz| - a_max - (onGround[prev] ? 0.2 : 0) - 0.003; with several matching candidates the
// most lenient one counts.

const (
	idVelocityVertical   = "velocity.vertical"
	idVelocityHorizontal = "velocity.horizontal"
)

func init() {
	Register(Factory{
		Name:   "velocity",
		Checks: []string{idVelocityVertical, idVelocityHorizontal},
		New:    func() Detector { return &velocityDetector{} },
	})
}

// envExclusionsVelocity are the environments in which knockback physics are not modelled (CHECKS.md 2.9),
// plus the env side of "gliding/flying/in a vehicle".
var envExclusionsVelocity = []pb.EnvFlag{
	pb.EnvFlag_ENV_FLAG_IN_WATER, pb.EnvFlag_ENV_FLAG_IN_LAVA, pb.EnvFlag_ENV_FLAG_IN_WEB,
	pb.EnvFlag_ENV_FLAG_CLIMBING, pb.EnvFlag_ENV_FLAG_IN_POWDER_SNOW, pb.EnvFlag_ENV_FLAG_IN_BUBBLE_COLUMN,
	pb.EnvFlag_ENV_FLAG_ON_HONEY, pb.EnvFlag_ENV_FLAG_STUCK, pb.EnvFlag_ENV_FLAG_GLIDING,
	pb.EnvFlag_ENV_FLAG_IN_VEHICLE, pb.EnvFlag_ENV_FLAG_RIPTIDING,
}

const velMoveExclusions = uint32(pb.MoveFlag_MOVE_FLAG_GLIDING | pb.MoveFlag_MOVE_FLAG_FLYING |
	pb.MoveFlag_MOVE_FLAG_IN_VEHICLE | pb.MoveFlag_MOVE_FLAG_RIPTIDE | pb.MoveFlag_MOVE_FLAG_TELEPORT_PENDING)

type velocityDetector struct {
	env      envClock
	p        velPending
	vertical outcomeBits // true = ignored
	horiz    outcomeBits // true = shortfall > limit
}

// velPending is the velocity whose window is open.
type velPending struct {
	active       bool
	endUs        int64
	vx, vy, vz   float64
	groundBefore bool
	sawMove      bool
	consecutive  int // consecutive position moves seen in the window
	matched      bool
	hJudged      bool // some matching candidate was judged horizontally
	hBlocked     bool // some matching candidate collided horizontally (not judgeable)
	shortfall    float64
	h, hMin, vxz float64
}

func (d *velocityDetector) Handle(c *Ctx, ev *pb.Event) {
	v := c.View
	d.env.observe(v, ev)
	if d.p.active && v.NowUs > d.p.endUs {
		d.finish(c)
	}
	switch k := ev.GetKind().(type) {
	case *pb.Event_Join:
		if !k.Join.GetUpdate() {
			d.reset()
		}
	case *pb.Event_Quit, *pb.Event_Teleport:
		d.p = velPending{}
	case *pb.Event_Velocity:
		d.onVelocity(c, k.Velocity)
	case *pb.Event_Move:
		if d.p.active {
			d.onMove(c)
		}
	}
}

func (d *velocityDetector) reset() { *d = velocityDetector{} }

// windowUs is rttHigh + extra (CHECKS.md 2.9) in microseconds.
func velWindowUs(c *Ctx) int64 {
	v := c.View
	rtt := float64(v.RTTHighMs)
	if rtt == 0 {
		rtt = float64(v.PingMs)
	}
	if rtt == 0 {
		rtt = 100 // ARCHITECTURE 4.3: ping fallback
	}
	return int64((rtt + c.Param(idVelocityVertical, "window_extra_ms", 400)) * 1000)
}

func (d *velocityDetector) eligible(v *View) bool {
	if !v.Judgeable() || v.Flying || v.Flags&velMoveExclusions != 0 {
		return false
	}
	for _, f := range envExclusionsVelocity {
		if d.env.recently(v, f, time1s) {
			return false
		}
	}
	return true
}

func (d *velocityDetector) onVelocity(c *Ctx, vel *pb.VelocityApplied) {
	v := c.View
	if vel.GetSource() == pb.VelocityApplied_SOURCE_EXPLOSION {
		// Explosions add to the motion; nothing is judged around them (View records the time).
		d.p = velPending{}
		return
	}
	if d.p.active {
		if d.p.matched {
			d.finish(c)
		}
		d.p = velPending{}
	}
	window := velWindowUs(c)
	if !(vel.GetVy() > c.Param(idVelocityVertical, "min_vy", 0.15)) || v.Since(v.LastExplosionUs) <= window || !d.eligible(v) {
		return
	}
	d.p = velPending{active: true, endUs: v.NowUs + window, vx: vel.GetVx(), vy: vel.GetVy(), vz: vel.GetVz(),
		groundBefore: vel.GetOnGroundBefore()}
}

func (d *velocityDetector) onMove(c *Ctx) {
	v := c.View
	if !d.eligible(v) {
		d.p = velPending{}
		return
	}
	first := !d.p.sawMove
	d.p.sawMove = true
	cur, _ := v.Moves.Last()
	prev, ok := v.Moves.FromEnd(1)
	if !ok || !Consecutive(prev, cur, v.TickClient) {
		return
	}
	d.p.consecutive++
	prevGround := prev.OnGround || first && d.p.groundBefore
	dy := cur.Pos.Y - prev.Pos.Y
	jump := v.JumpStrength
	if amp, ok := v.Effect("minecraft:jump_boost"); ok {
		jump += 0.1 * float64(amp+1)
	}
	eps := c.Param(idVelocityVertical, "match_eps", 0.012)
	if !velocityMatches(dy, d.p.vy, jump, eps, prevGround, d.env.recently(v, pb.EnvFlag_ENV_FLAG_LOW_CEILING, time1s)) {
		return
	}
	d.p.matched = true

	vxz := math.Hypot(d.p.vx, d.p.vz)
	if vxz < c.Param(idVelocityHorizontal, "min_vxz", 0.3) {
		return
	}
	// Tick clients report horizontal collisions in the movement packet; for older clients any solid block
	// near the hitbox makes the move unjudgeable.
	if cur.HorizontalCollision || !v.TickClient && d.env.recently(v, pb.EnvFlag_ENV_FLAG_NEAR_SOLID, time1s) {
		d.p.hBlocked = true
		return
	}
	h := cur.Pos.Sub(prev.Pos).HorizontalLen()
	s, hMin := velocityShortfall(vxz, h, v.MovementSpeed, prevGround)
	if !d.p.hJudged || s < d.p.shortfall {
		d.p.shortfall, d.p.h, d.p.hMin, d.p.vxz = s, h, hMin, vxz
	}
	d.p.hJudged = true
}

// velocityMatches is the vertical match rule of CHECKS.md 2.9.
func velocityMatches(dy, vy, jumpPower, eps float64, prevGround, lowCeiling bool) bool {
	if math.Abs(vy) < 0.003 {
		vy = 0
	}
	switch {
	case math.Abs(dy-vy) < eps:
		return true
	case prevGround && math.Abs(dy-math.Max(vy, jumpPower)) < eps: // jump reset
		return true
	case lowCeiling && dy <= vy+eps:
		return true
	}
	return false
}

// velocityShortfall is the horizontal rule of CHECKS.md 2.10. movementSpeed is the attribute without the
// sprint modifier.
func velocityShortfall(vxz, h, movementSpeed float64, prevGround bool) (s, hMin float64) {
	aMax, jump := 0.026, 0.0
	if prevGround {
		aMax, jump = movementSpeed*1.3, 0.2
	}
	hMin = math.Max(0, vxz-aMax-jump-0.003)
	if h < hMin && vxz > 0 {
		s = (hMin - h) / vxz
	}
	return s, hMin
}

func (d *velocityDetector) finish(c *Ctx) {
	p := d.p
	d.p = velPending{}
	switch {
	case p.matched:
		d.judgeVertical(c, false, p)
		if p.hJudged && !p.hBlocked {
			d.judgeHorizontal(c, p)
		}
	case p.consecutive >= 3:
		d.judgeVertical(c, true, p)
	}
}

func (d *velocityDetector) judgeVertical(c *Ctx, ignored bool, p velPending) {
	d.vertical.push(ignored)
	need := c.Param(idVelocityVertical, "need", 4)
	of := int(c.Param(idVelocityVertical, "of", 6))
	n := d.vertical.count(of)
	if !ignored || float64(n) < need {
		return
	}
	d.vertical.reset()
	c.Emit(Emission{Check: idVelocityVertical, Points: 6, Confidence: 0.85,
		Info: fmt.Sprintf("ignored %d of the last %d knockbacks (vy %.3f)", n, of, p.vy),
		Data: map[string]float64{"vy": p.vy, "ignored": float64(n), "of": float64(of), "moves": float64(p.consecutive)}})
}

func (d *velocityDetector) judgeHorizontal(c *Ctx, p velPending) {
	limit := c.Param(idVelocityHorizontal, "shortfall", 0.15)
	bad := p.shortfall > limit
	d.horiz.push(bad)
	need := c.Param(idVelocityHorizontal, "need", 4)
	of := int(c.Param(idVelocityHorizontal, "of", 6))
	n := d.horiz.count(of)
	if !bad || float64(n) < need {
		return
	}
	d.horiz.reset()
	c.Emit(Emission{Check: idVelocityHorizontal, Points: 5, Confidence: 0.8,
		Info: fmt.Sprintf("took %.0f%% of the expected horizontal knockback on %d of the last %d", 100*(1-p.shortfall), n, of),
		Data: map[string]float64{"shortfall": p.shortfall, "h": p.h, "h_min": p.hMin, "v_xz": p.vxz, "count": float64(n), "of": float64(of)}})
}

// ---------------------------------------------------------------------------------------------
// Helpers shared by the velocity, autoclicker, scaffold, dig, inventory and latency detectors.
// ---------------------------------------------------------------------------------------------

const (
	time1s = int64(1_000_000) // microseconds
	time5s = 5 * time1s
)

// outcomeBits is the history of the newest (up to 64) yes/no outcomes of a check, newest in bit 0. It backs
// the "k of the last n" rules without allocating.
type outcomeBits struct {
	bits uint64
	n    int
}

func (o *outcomeBits) push(b bool) {
	o.bits <<= 1
	if b {
		o.bits |= 1
	}
	if o.n < 64 {
		o.n++
	}
}

// count returns how many of the newest `last` outcomes were true.
func (o *outcomeBits) count(last int) int {
	last = min(max(last, 1), o.n)
	if last == 0 {
		return 0
	}
	mask := ^uint64(0)
	if last < 64 {
		mask = 1<<uint(last) - 1
	}
	return bits.OnesCount64(o.bits & mask)
}

// size is the number of recorded outcomes (max 64).
func (o *outcomeBits) size() int { return o.n }

func (o *outcomeBits) reset() { *o = outcomeBits{} }

// envClock tracks, per EnvFlag, when the flag was last known to be set. View.EnvSetAtUs records the last
// sample that carried a flag; because the plugin samples on change and otherwise only every 5 s, a flag
// that is cleared by a sample was set until shortly before that sample, so the clearing sample's time is
// also recorded here. recently combines both.
type envClock struct {
	prev   uint32
	seen   bool
	lastUs [32]int64
}

func (e *envClock) observe(v *View, ev *pb.Event) {
	env, ok := ev.GetKind().(*pb.Event_Env)
	if !ok {
		return
	}
	flags := env.Env.GetFlags()
	if e.seen {
		for f := e.prev &^ flags; f != 0; f &= f - 1 { // cleared by this sample
			e.lastUs[bits.TrailingZeros32(f)] = v.NowUs
		}
	}
	e.prev, e.seen = flags, true
}

// recently reports whether flag was set within withinUs of now (or is set).
func (e *envClock) recently(v *View, flag pb.EnvFlag, withinUs int64) bool {
	if v.EnvRecently(flag, withinUs) {
		return true
	}
	if uint32(flag) == 0 {
		return false
	}
	at := e.lastUs[bits.TrailingZeros32(uint32(flag))]
	return at != 0 && v.NowUs-at <= withinUs
}

// timeMarks keeps the times of the newest violations-in-the-making of a "k within t" rule.
type timeMarks struct{ r Ring[int64] }

func (m *timeMarks) add(at int64) {
	if m.r.Cap() <= 1 {
		m.r = NewRing[int64](32)
	}
	m.r.Push(at)
}

// within counts the marks no older than windowUs before now.
func (m *timeMarks) within(now, windowUs int64) int {
	n := 0
	for i := m.r.Len() - 1; i >= 0; i-- {
		if now-m.r.At(i) > windowUs {
			break
		}
		n++
	}
	return n
}

func (m *timeMarks) reset() { m.r.Reset() }
