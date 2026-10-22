package checks

import (
	"math"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Combat geometry and timing shared by the cloud checks (CHECKS.md 2.1): the rotation of an action
// (R0, R1 and their interpolations), the attack timing window over the target's received positions,
// target box and eye candidates, and the skip rules. reach, killaura and aim use it for attacks; the
// placement and dig checks can use PendingR1 and ActionRotations for their R0/R1 candidates.

const (
	// TargetMargin is the expansion m of every target box candidate.
	TargetMargin = 0.1
	// HitboxTimingMargin is killaura.hitbox's extra box expansion for rotation timing.
	HitboxTimingMargin = 0.1
	// R1TimeoutUs bounds the wait for R1: an action without a same-tick move (tick clients) or a move
	// within this time (other clients) uses R1 = R0.
	R1TimeoutUs = 100_000
	// TeleportSkipUs: actions within this time after a teleport of either player are skipped.
	TeleportSkipUs = 1_000_000
	// MaxJudgedPingMs: attacks by players with a higher ping are skipped.
	MaxJudgedPingMs = 600

	attackWindowSlackUs = 300_000 // the 300 ms of the attack timing window
	defaultRTTMs        = 100     // when the plugin reported no RTT at all
	// targetTeleportBlocks: a jump of the target between two received positions larger than this is a
	// teleport (no movement covers it in one sample interval).
	targetTeleportBlocks = 8.0
	pluginTickUs         = 50_000
)

// Attack skip reasons (CHECKS.md 2.1 "Skip").
const (
	SkipState          = "state" // bedrock/exempt, dead, offline
	SkipGameMode       = "game_mode"
	SkipVehicle        = "vehicle"
	SkipTargetVehicle  = "target_vehicle"
	SkipExtendedReach  = "extended_reach"
	SkipStab           = "stab"
	SkipPing           = "ping"
	SkipTeleport       = "teleport"
	SkipTargetTeleport = "target_teleport"
	SkipStall          = "stall"
	SkipNotPlayer      = "not_player"
	SkipNoHistory      = "no_history"
)

// TimedPoint is a received target position on the plugin clock.
type TimedPoint struct {
	TimeUs int64
	Pos    geom.Vec3
}

// AttackObs is the copy of one Attack (and the attacker's state at it) that the combat checks keep
// after the event is gone.
type AttackObs struct {
	TimeUs     int64 // normalised time of the attack
	PluginUs   int64 // Event.time_us (plugin clock, the clock of TargetHistory)
	Tick       uint32
	TickClient bool
	Protocol   int32

	TargetID        int32
	TargetPlayer    [16]byte
	HasTargetPlayer bool
	Eye             geom.Vec3
	EyeHeight       float64
	Sneaking        bool
	SneakUncertain  bool      // the sneaking state changed around the attack: test both eye heights
	PrevFeet        geom.Vec3 // position before the last one (eyePath), when HasPrev
	HasPrev         bool
	R0              geom.Rotation
	R0Server        bool // the rotation in force at the attack was set by the server
	TargetW         float64
	TargetH         float64
	History         []TimedPoint // oldest first
	PingMs          uint32
	RTTHighMs       uint32
	Reach           float64 // entity interaction range
	Scale           float64
	ServerTickMs    float64

	GameMode          pb.GameMode
	Judgeable         bool // View.Judgeable at the attack
	AttackerInVehicle bool
	TargetInVehicle   bool
	ExtendedReach     bool
	Stab              bool
	SinceTeleportUs   int64 // time since the attacker's last teleport (very large when none)
	InStall           bool  // a server stall overlaps the timing window
}

// NewAttackObs copies the attack of ev; v must already have applied ev. ok is false when ev is not
// an Attack.
func NewAttackObs(v *View, ev *pb.Event) (AttackObs, bool) {
	a := ev.GetAttack()
	if a == nil {
		return AttackObs{}, false
	}
	o := AttackObs{
		TimeUs: v.NowUs, PluginUs: int64(ev.GetTimeUs()), Tick: ev.GetTick(), TickClient: v.TickClient, Protocol: v.Protocol,
		TargetID: a.GetTargetEntityId(), Sneaking: a.GetSneaking(),
		R0:      geom.Rotation{Yaw: float64(a.GetYaw()), Pitch: float64(a.GetPitch())},
		TargetW: float64(a.GetTargetWidth()), TargetH: float64(a.GetTargetHeight()),
		PingMs: a.GetPingMs(), RTTHighMs: a.GetRttHighMs(), Reach: float64(a.GetEntityReach()),
		Scale: v.Scale, ServerTickMs: v.ServerTickMs, GameMode: a.GetGameMode(), Judgeable: v.Judgeable(),
		AttackerInVehicle: a.GetAttackerInVehicle() || v.InVehicle, TargetInVehicle: a.GetTargetInVehicle(),
		ExtendedReach: a.GetExtendedReach() || v.EnvRecently(pb.EnvFlag_ENV_FLAG_EXTENDED_REACH_ITEM, 0),
		Stab:          a.GetStab(), SinceTeleportUs: v.Since(v.LastTeleportUs),
	}
	if tp := a.GetTargetPlayer(); len(tp) == 16 {
		copy(o.TargetPlayer[:], tp)
		o.HasTargetPlayer = true
	}
	if e := a.GetEye(); e != nil {
		o.Eye = geom.V(e.GetX(), e.GetY(), e.GetZ())
	} else {
		o.Eye = geom.Eye(v.Pos, v.Pose(), v.Protocol, v.Scale)
	}
	o.EyeHeight = float64(a.GetEyeHeight())
	if o.EyeHeight <= 0 {
		o.EyeHeight = geom.EyeHeight(v.Pose(), v.Protocol, v.Scale)
	}
	if o.Reach <= 0 {
		o.Reach = v.EntityReach
	}
	o.PrevFeet, o.HasPrev = v.PrevFeet()
	if o.GameMode == pb.GameMode_GAME_MODE_SURVIVAL {
		o.GameMode = v.GameMode // the attack's default value says nothing; the view knows better
	}
	o.SneakUncertain = cwSneakUncertain(v, o.Sneaking)
	o.R0Server = LastRotationFromServer(v)
	if h := a.GetTargetHistory(); len(h) > 0 {
		o.History = make([]TimedPoint, 0, len(h))
		for _, p := range h {
			o.History = append(o.History, TimedPoint{TimeUs: int64(p.GetTimeUs()), Pos: geom.V(p.GetX(), p.GetY(), p.GetZ())})
		}
	}
	start, end := o.Window()
	offset := v.NowUs - o.PluginUs
	o.InStall = v.InStall(start+offset, end+offset)
	return o, true
}

// cwSneakUncertain reports whether the attacker's sneaking state may have changed around the attack:
// the attack disagrees with the newest move, or the last moves disagree with each other.
func cwSneakUncertain(v *View, attackSneaking bool) bool {
	sneak := uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING)
	for k := 0; k < 3; k++ {
		m, ok := v.Moves.FromEnd(k)
		if !ok {
			break
		}
		if (m.Flags&sneak != 0) != attackSneaking {
			return true
		}
	}
	return false
}

// LastRotationFromServer reports whether the rotation currently in force (that of the newest
// rotation-bearing move) was set by the server (MOVE_FLAG_SERVER_ROTATION).
func LastRotationFromServer(v *View) bool {
	for k := 0; k < v.Moves.Len(); k++ {
		m, _ := v.Moves.FromEnd(k)
		if m.HasRot {
			return m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
		}
	}
	return false
}

// RTTHigh returns the RTT bound of the timing window: the attack's rtt_high, else its ping, else
// 100 ms.
func (a *AttackObs) RTTHigh() uint32 {
	switch {
	case a.RTTHighMs > 0:
		return max(a.RTTHighMs, a.PingMs)
	case a.PingMs > 0:
		return a.PingMs
	}
	return defaultRTTMs
}

// Window returns the attack timing window [T - rttHigh - 2*serverTickMs - 300 ms, T] on the plugin
// clock (the clock of the target history).
func (a *AttackObs) Window() (startUs, endUs int64) {
	tick := a.ServerTickMs
	if !(tick > 0) {
		tick = 50
	}
	back := int64(a.RTTHigh())*1000 + int64(2*tick*1000) + attackWindowSlackUs
	return a.PluginUs - back, a.PluginUs
}

// targetSize returns the largest box the target can have: its reported width (0.6 x scale) and the
// standing height for that width (3 x width), or the reported height if larger.
func (a *AttackObs) targetSize() (w, h float64) {
	w = a.TargetW
	if !(w > 0) || math.IsInf(w, 0) {
		w = geom.PlayerWidth
	}
	h = math.Max(a.TargetH, w*geom.PlayerHeight/geom.PlayerWidth)
	if math.IsInf(h, 0) || math.IsNaN(h) {
		h = geom.PlayerHeight
	}
	return w, h
}

// windowPoints returns the target positions relevant to the timing window: every position received
// inside it plus the one in force when it opened, oldest first.
func (a *AttackObs) windowPoints() []TimedPoint {
	start, end := a.Window()
	first, last := -1, -1
	for i, p := range a.History {
		if p.TimeUs > end {
			break
		}
		last = i
		if p.TimeUs <= start || first < 0 {
			first = i
		}
	}
	if last < 0 {
		if len(a.History) == 0 {
			return nil
		}
		return a.History[:1] // every position is newer than the attack: use the oldest
	}
	return a.History[first : last+1]
}

// TargetSamples returns the sampled target feet positions of the timing window: each segment between
// consecutive positions at 0, 0.25, 0.5, 0.75 and 1 (consecutive duplicates removed).
func (a *AttackObs) TargetSamples() []geom.Vec3 {
	pts := a.windowPoints()
	if len(pts) == 0 {
		return nil
	}
	out := make([]geom.Vec3, 0, 4*len(pts)+1)
	add := func(p geom.Vec3) {
		if n := len(out); n == 0 || out[n-1] != p {
			out = append(out, p)
		}
	}
	add(pts[0].Pos)
	for i := 1; i < len(pts); i++ {
		p0, p1 := pts[i-1].Pos, pts[i].Pos
		for _, f := range [...]float64{0.25, 0.5, 0.75} {
			add(p0.Lerp(p1, f))
		}
		add(p1)
	}
	return out
}

// TargetBoxes returns the target box candidates of the timing window, each the largest possible box
// at a sampled position expanded by margin.
func (a *AttackObs) TargetBoxes(margin float64) []geom.AABB {
	w, h := a.targetSize()
	samples := a.TargetSamples()
	out := make([]geom.AABB, len(samples))
	for i, s := range samples {
		out[i] = geom.EntityBox(s, w, h).Expand(margin)
	}
	return out
}

// EyeCandidates returns the attacker eye positions to test: the reported eye, the eyes on the path
// from the previous position (eyePath) and, when the sneaking state is uncertain, the standing and
// sneaking eye at the same feet (and on the path).
func (a *AttackObs) EyeCandidates() []geom.Vec3 {
	out := []geom.Vec3{a.Eye}
	feet := geom.V(a.Eye.X, a.Eye.Y-a.EyeHeight, a.Eye.Z)
	heights := []float64{a.EyeHeight}
	if a.SneakUncertain {
		for _, pose := range [...]geom.Pose{geom.PoseStanding, geom.PoseSneaking} {
			if e := geom.Eye(feet, pose, a.Protocol, a.Scale); e != a.Eye {
				out = append(out, e)
				heights = append(heights, e.Y-feet.Y)
			}
		}
	}
	if a.HasPrev {
		for _, h := range heights {
			out = append(out, eyePath(a.PrevFeet, feet, h)...)
		}
	}
	return out
}

// EyeUncertainty is the eye position uncertainty of the attacker's client version.
func (a *AttackObs) EyeUncertainty() float64 { return geom.EyeUncertainty(a.Protocol) }

// TargetTeleported reports whether the target history shows a teleport (a jump of more than 8 blocks
// between consecutive positions) within TeleportSkipUs before the attack.
func (a *AttackObs) TargetTeleported() bool {
	for i := 1; i < len(a.History); i++ {
		p0, p1 := a.History[i-1], a.History[i]
		if p1.TimeUs < a.PluginUs-TeleportSkipUs || p1.TimeUs > a.PluginUs {
			continue
		}
		if p0.Pos.Dist(p1.Pos) > targetTeleportBlocks {
			return true
		}
	}
	return false
}

// SkipReason applies CHECKS.md 2.1 "Skip" and returns "" when the attack may be judged. geometric
// adds the rules of the reach and hitbox checks (extended reach and stab attacks, server stalls over
// the timing window) and requires a tracked player target with a position history. Rotation samples
// marked SERVER_ROTATION are judged by the caller (they depend on R1).
func (a *AttackObs) SkipReason(geometric bool) string {
	switch {
	case !a.Judgeable:
		return SkipState
	case a.GameMode == pb.GameMode_GAME_MODE_CREATIVE || a.GameMode == pb.GameMode_GAME_MODE_SPECTATOR:
		return SkipGameMode
	case a.AttackerInVehicle:
		return SkipVehicle
	case a.TargetInVehicle:
		return SkipTargetVehicle
	case a.PingMs > MaxJudgedPingMs:
		return SkipPing
	case a.SinceTeleportUs < TeleportSkipUs:
		return SkipTeleport
	}
	if !geometric {
		return ""
	}
	switch {
	case a.Stab:
		return SkipStab
	case a.ExtendedReach:
		return SkipExtendedReach
	case !a.HasTargetPlayer:
		return SkipNotPlayer
	case len(a.History) == 0:
		return SkipNoHistory
	case a.TargetTeleported():
		return SkipTargetTeleport
	case a.InStall:
		return SkipStall
	}
	return ""
}

// ReachDistance returns reach.distance's dist (the minimum distance from any eye candidate to any
// target box candidate of the window, 0 inside) and limit (entity reach + eye uncertainty). ok is
// false without target positions.
func (a *AttackObs) ReachDistance() (dist, limit float64, ok bool) {
	boxes := a.TargetBoxes(TargetMargin)
	if len(boxes) == 0 {
		return 0, 0, false
	}
	dist = math.Inf(1)
	for _, eye := range a.EyeCandidates() {
		for _, b := range boxes {
			if d := b.DistanceTo(eye); d < dist {
				dist = d
			}
		}
	}
	if math.IsInf(dist, 0) || math.IsNaN(dist) {
		return 0, 0, false
	}
	return dist, a.Reach + a.EyeUncertainty(), true
}

// ActionRotations returns the rotation candidates of an action: R0, the R0->R1 interpolations at
// 0.25 steps along the shortest angular path, and R1 (only R0 when they are equal).
func ActionRotations(r0, r1 geom.Rotation) []geom.Rotation {
	if r0 == r1 {
		return []geom.Rotation{r0}
	}
	return geom.RotationCandidates(r0, r1)
}

// AnyRayHits reports whether any ray from an eye candidate along a rotation candidate enters any box
// within maxDist (math.Inf(1) for no limit).
func AnyRayHits(eyes []geom.Vec3, rots []geom.Rotation, boxes []geom.AABB, maxDist float64) bool {
	for _, r := range rots {
		dir := r.Look()
		for _, eye := range eyes {
			for _, b := range boxes {
				if _, hit := b.IntersectRay(eye, dir, maxDist); hit {
					return true
				}
			}
		}
	}
	return false
}

// HitboxMiss decides killaura.hitbox for a resolved attack: miss when no (eye, rotation) candidate
// ray intersects any target box candidate expanded by the timing margin. ok is false when the attack
// is not judged (skip rules, server-set rotations).
func HitboxMiss(e R1Entry[AttackObs]) (miss, ok bool) {
	a := &e.Item
	if a.SkipReason(true) != "" || a.R0Server || e.R1Server {
		return false, false
	}
	boxes := a.TargetBoxes(TargetMargin + HitboxTimingMargin + a.EyeUncertainty())
	if len(boxes) == 0 {
		return false, false
	}
	return !AnyRayHits(a.EyeCandidates(), ActionRotations(e.R0, e.R1), boxes, math.Inf(1)), true
}

// PrecisionAngle returns aim.precision's angle for a resolved attack: the angle between the attack
// rotation (R1 for tick clients, the attack's own rotation otherwise) and the direction from the eye
// to the target box centre at the received target position closest to T - ping/2. ok is false when
// the attack is not eligible: skipped, server-set rotation, target distance outside 1.5..4 or target
// moving at most 0.15 blocks per tick.
func PrecisionAngle(e R1Entry[AttackObs]) (angle float64, ok bool) {
	a := &e.Item
	if a.SkipReason(true) != "" {
		return 0, false
	}
	rot, server := e.R0, a.R0Server
	if a.TickClient {
		ownRot := e.R1Move && e.Move.HasRot // else R1 = R0, the rotation already in force
		rot, server = e.R1, e.R1Server || (!ownRot && a.R0Server)
	}
	if server {
		return 0, false
	}
	at := a.PluginUs - int64(a.PingMs)*500
	best := -1
	for i, p := range a.History {
		if best < 0 || cwAbs64(p.TimeUs-at) < cwAbs64(a.History[best].TimeUs-at) {
			best = i
		}
	}
	if best < 0 || cwTargetSpeed(a.History, best) <= 0.15 {
		return 0, false
	}
	_, h := a.targetSize()
	centre := a.History[best].Pos.Add(geom.V(0, h/2, 0))
	to := centre.Sub(a.Eye)
	if d := to.Len(); d < 1.5 || d > 4 {
		return 0, false
	}
	return geom.AngleBetween(rot.Look(), to)
}

// cwTargetSpeed estimates the target's speed (blocks per tick) at history index i from the segments
// adjacent to it.
func cwTargetSpeed(h []TimedPoint, i int) float64 {
	sum, n := 0.0, 0
	for _, j := range [...]int{i - 1, i + 1} {
		if j < 0 || j >= len(h) {
			continue
		}
		a, b := h[min(i, j)], h[max(i, j)]
		dt := float64(b.TimeUs-a.TimeUs) / pluginTickUs
		if dt <= 0 {
			continue
		}
		sum += a.Pos.Dist(b.Pos) / dt
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func cwAbs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// PendingR1 resolves R1 (CHECKS.md 2.1) for the actions of one player. Tick clients take the
// rotation of the next movement packet with the same tick number (none in that tick: the rotation did
// not change, R1 = R0); other clients the next move within 100 ms. Actions are resolved by the move
// that provides R1, by an event of a later tick, after R1TimeoutUs, or by the player's quit.
type PendingR1[T any] struct {
	q []R1Entry[T]
}

// R1Entry is a resolved (or pending) action.
type R1Entry[T any] struct {
	Item     T
	TimeUs   int64  // normalised time of the action
	Tick     uint32 // client tick of the action (0 for clients without tick ends)
	R0       geom.Rotation
	R1       geom.Rotation
	R1Move   bool // R1 is the rotation of Move (else R1 = R0)
	R1Server bool // R1's move carried a server-set rotation
	Move     Move // the move that provided R1 (when R1Move)
}

// Len returns the number of pending actions.
func (p *PendingR1[T]) Len() int { return len(p.q) }

// Add queues an action performed with rotation r0 at the view's current event.
func (p *PendingR1[T]) Add(v *View, item T, r0 geom.Rotation) {
	p.q = append(p.q, R1Entry[T]{Item: item, TimeUs: v.NowUs, Tick: v.Tick, R0: r0, R1: r0})
}

// Resolve returns the actions that ev completes, oldest first. Call it for every event of the player
// after View.Apply and before adding the event's own action.
func (p *PendingR1[T]) Resolve(v *View, ev *pb.Event) []R1Entry[T] {
	if len(p.q) == 0 {
		return nil
	}
	_, quit := ev.GetKind().(*pb.Event_Quit)
	var mv Move
	isMove := false
	if _, ok := ev.GetKind().(*pb.Event_Move); ok {
		mv, isMove = v.Moves.Last()
	}
	var done []R1Entry[T]
	keep := p.q[:0]
	for _, e := range p.q {
		resolved := quit
		switch {
		case resolved:
		case isMove && v.TickClient && e.Tick != 0:
			if mv.Tick == e.Tick {
				e.setMove(mv)
				resolved = true
			} else if mv.Tick > e.Tick || v.NowUs-e.TimeUs > R1TimeoutUs {
				resolved = true
			}
		case isMove:
			if v.NowUs-e.TimeUs <= R1TimeoutUs {
				e.setMove(mv)
			}
			resolved = true
		default:
			resolved = v.NowUs-e.TimeUs > R1TimeoutUs || (v.TickClient && e.Tick != 0 && v.Tick > e.Tick)
		}
		if resolved {
			done = append(done, e)
		} else {
			keep = append(keep, e)
		}
	}
	p.q = keep
	return done
}

func (e *R1Entry[T]) setMove(m Move) {
	e.R1Move, e.Move = true, m
	if m.HasRot {
		e.R1 = m.Rot
		e.R1Server = m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION)
	}
}

// cwCountTrue counts the true values of a ring.
func cwCountTrue(r *Ring[bool]) int {
	n := 0
	for i := 0; i < r.Len(); i++ {
		if r.At(i) {
			n++
		}
	}
	return n
}
