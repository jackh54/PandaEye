package ml

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// SessionInfo identifies the session whose windows a Session computes (FEATURES.md section 5).
type SessionInfo struct {
	TenantID  string
	Player    [16]byte
	ServerID  string
	SessionID string
	Protocol  int32
	HotLoaded bool
}

// Session computes the feature windows of one player session (ml/FEATURES.md, version 1). The worker
// calls Observe for every event of the session after View.Apply and the detectors (so state-based
// conditions read the derived state at the event), plus the server's ServerStall events; finalised
// windows come back from Observe, Housekeep and Close. Not safe for concurrent use.
type Session struct {
	info SessionInfo
	tick bool
	gcd  checks.GCDSource // the session's aim detector, nil when absent
	now  func() time.Time

	wins      map[int64]*win
	finalised int64 // newest finalised window start; windows at or before it are never reopened

	// Context.
	envHave    bool
	envTickMs  float32
	envSpecial bool
	stalls     []span // normalised server stalls of the session's server

	// Rotation pairs.
	moves          int
	afterTeleport  int
	prevCombatPair bool
	prevDyaw       float64

	// Combat items waiting for R1, ticks, swings.
	attacks   checks.PendingR1[atkItem]
	places    checks.PendingR1[placeItem]
	digs      checks.PendingR1[digItem]
	snap      *checks.SnapTicks
	snapQueue []tickItem
	curTick   uint32
	ticks     map[uint32]*tickRec
	swings    []swingItem
	digActive bool
	digEnd    uint32 // tick of the last dig CANCEL/FINISH

	// Velocity response window (velocity.vertical / velocity.horizontal machinery).
	vel *velItem

	// Latency pairing.
	pongs []timed
	kas   []kaItem

	// Ticks: timer offsets and gaps.
	timerBase     bool
	baseUs        uint64
	baseTick      uint32
	lastTickedT   int64
	lastTickedTk  uint32
	haveTicked    bool
	bursts        []*gapItem
	placeTimes    []int64
	digStartTimes []int64
	checkStats    map[string][]statItem
}

type span struct{ start, end int64 }

type timed struct {
	t int64
	x float64
}

type atkItem struct {
	obs   checks.AttackObs
	win   int64
	reach float64 // Attack.entity_reach, 3 when 0
}

type placeItem struct {
	ev  *pb.BlockPlace
	win int64
	eye geom.Vec3
}

type digItem struct {
	ev  *pb.BlockDig
	win int64
	eye geom.Vec3
}

type tickItem struct {
	tick uint32
	win  int64
}

type tickRec struct {
	finished   bool // decided; kept only for the next tick's screen-open test
	placeOrUse bool
	mainSwing  bool
	invClick   bool
	attacks    int // non-stab attacks
	targets    map[int32]struct{}
	attackWin  int64
	haveAttack bool
	invMove    bool
	invRot     bool
	moveWin    int64
	haveMove   bool
}

type swingItem struct {
	t        int64
	tick     uint32
	win      int64
	near     bool // an attack within 1.5 s
	tickDone bool
	excluded bool
}

type velItem struct {
	t              int64
	win            int64
	vx, vy, vz     float64
	onGroundBefore bool
	end            int64
	consecutive    int
	firstDy        float64
	haveFirst      bool
}

type kaItem struct {
	t   int64
	rtt float64
	win int64
}

type gapItem struct {
	ta, tb   int64
	tickB    uint32
	lastTick uint32 // newest tick of events up to tb + 100 ms
	decided  bool
	burst    bool
}

type statItem struct {
	t int64
	s *pb.CheckStats
}

// win accumulates one window.
type win struct {
	start, end int64

	nEvents       int
	tFirst, tLast int64
	moves, focus  int

	envN, envSpecial int
	envTickMax       float32
	fbHave           bool // latest EnvSample before the window
	fbTickMs         float32
	fbSpecial        bool

	clicks map[uint32]int

	cYaw, cPitch, cAccel, iYaw, iPitch []float64
	gcdCN, gcdCOn, gcdIN, gcdION       int

	snapTicks, snaps int
	precision        []float64

	attacks, eligible int
	targets           map[int32]struct{}
	reach             []float64
	hitboxMiss        int
	noswingN          int
	noswingMiss       int
	multiTicks        int

	velN, velIgnored int
	velRatios        []float64
	jrN, jrOnly      int
	velHN, velHShort int
	velHRatios       []float64

	flags     [15]int
	moveSev   float64
	moveSevOK bool

	timerN   [15]int
	timerMin [15]float64

	places, placeElig, faceInvalid, rayMiss, cursorN, cursorMis int
	placeReach                                                  []float64
	placePeak                                                   int

	digs, digElig, digMiss, digPeak int

	invClicks  int
	invMinMs   float64
	invHave    bool
	lastInvT   int64
	invHaveT   bool
	invMove    int
	invRot     int
	pongs, kas []float64
	kaDeltas   []float64

	gaps []*gapItem
}

// Window rules (FEATURES.md 2-3).
const (
	clickAttackUs    = 1_500_000
	clickDigTicks    = 6
	clickMaxInterval = 10
	combatPairUs     = 1_000_000
	idlePairUs       = 2_000_000
	kaPairUs         = 2_000_000
	gapUs            = 500_000
	burstUs          = 100_000
	burstTicks       = 10
	statsBackUs      = 330_000_000
	timerBinUs       = 2_000_000
	timerBins        = 15
	velEps           = 0.012
	velExtraUs       = 400_000
	velEnvUs         = 1_000_000
)

var envSpecialMask = uint32(pb.EnvFlag_ENV_FLAG_IN_WATER | pb.EnvFlag_ENV_FLAG_IN_LAVA | pb.EnvFlag_ENV_FLAG_CLIMBING |
	pb.EnvFlag_ENV_FLAG_IN_WEB | pb.EnvFlag_ENV_FLAG_IN_POWDER_SNOW | pb.EnvFlag_ENV_FLAG_IN_BUBBLE_COLUMN |
	pb.EnvFlag_ENV_FLAG_ON_HONEY | pb.EnvFlag_ENV_FLAG_STUCK | pb.EnvFlag_ENV_FLAG_IN_VEHICLE |
	pb.EnvFlag_ENV_FLAG_GLIDING | pb.EnvFlag_ENV_FLAG_RIPTIDING)

// velocity.vertical eligibility: env flags in the last 1 s that exclude a velocity.
var velEnvExclusions = []pb.EnvFlag{pb.EnvFlag_ENV_FLAG_IN_WATER, pb.EnvFlag_ENV_FLAG_IN_LAVA, pb.EnvFlag_ENV_FLAG_IN_WEB,
	pb.EnvFlag_ENV_FLAG_CLIMBING, pb.EnvFlag_ENV_FLAG_IN_POWDER_SNOW, pb.EnvFlag_ENV_FLAG_IN_BUBBLE_COLUMN,
	pb.EnvFlag_ENV_FLAG_ON_HONEY, pb.EnvFlag_ENV_FLAG_STUCK}

// inventory.move: env flags that exclude a move.
var invEnvExclusions = []pb.EnvFlag{pb.EnvFlag_ENV_FLAG_IN_WATER, pb.EnvFlag_ENV_FLAG_IN_LAVA, pb.EnvFlag_ENV_FLAG_ON_ICE,
	pb.EnvFlag_ENV_FLAG_IN_BUBBLE_COLUMN, pb.EnvFlag_ENV_FLAG_IN_VEHICLE, pb.EnvFlag_ENV_FLAG_GLIDING}

// NewSession starts the windows of a session. gcd is the session's aim detector (its aim.gcd
// calibration feeds features 29-31); nil leaves those features at their defaults. now stamps
// Window.WorkerUnixUs (time.Now when nil).
func NewSession(info SessionInfo, gcd checks.GCDSource, now func() time.Time) *Session {
	if now == nil {
		now = time.Now
	}
	return &Session{
		info: info, tick: info.Protocol >= tickProtocol, gcd: gcd, now: now,
		wins: map[int64]*win{}, finalised: math.MinInt64, snap: checks.NewSnapTicks(),
		ticks: map[uint32]*tickRec{}, checkStats: map[string][]statItem{},
	}
}

// Info returns the session identity.
func (s *Session) Info() SessionInfo { return s.info }

func windowStart(t int64) int64 {
	return int64(math.Floor(float64(t)/WindowUs)) * WindowUs
}

// win returns the accumulator of t's window, creating it, or nil when that window was finalised.
func (s *Session) win(t int64) *win {
	start := windowStart(t)
	if start <= s.finalised {
		return nil
	}
	w := s.wins[start]
	if w == nil {
		w = &win{start: start, end: start + WindowUs, clicks: map[uint32]int{}, targets: map[int32]struct{}{},
			fbHave: s.envHave, fbTickMs: s.envTickMs, fbSpecial: s.envSpecial}
		s.wins[start] = w
	}
	return w
}

// at returns the open window that starts at start (nil when finalised or never opened).
func (s *Session) at(start int64) *win {
	if start <= s.finalised {
		return nil
	}
	return s.wins[start]
}

// Observe processes one event (after View.Apply and the detectors) and returns the windows it
// finalises: those whose end + G is at or before the event.
func (s *Session) Observe(ev *pb.Event, v *checks.View) []*Window {
	t := v.NowUs
	out := s.finaliseWhere(func(w *win) bool { return w.end+GraceUs <= t })
	if st := ev.GetStall(); st != nil {
		off := t - int64(ev.GetTimeUs())
		s.stalls = append(s.stalls, span{int64(st.GetStartUs()) + off, int64(st.GetEndUs()) + off})
		s.pruneStalls(t)
		return out
	}
	w := s.win(t)
	if w != nil {
		if w.nEvents == 0 || t < w.tFirst {
			w.tFirst = t
		}
		if w.nEvents == 0 || t > w.tLast {
			w.tLast = t
		}
		w.nEvents++
	}
	isMove := false
	if _, ok := ev.GetKind().(*pb.Event_Move); ok {
		isMove = true
		s.snap.ObserveMove(v)
	}

	// Decisions this event completes.
	for _, e := range s.attacks.Resolve(v, ev) {
		s.decideAttack(e)
	}
	for _, e := range s.places.Resolve(v, ev) {
		s.decidePlace(v, e)
	}
	for _, e := range s.digs.Resolve(v, ev) {
		s.decideDig(e)
	}
	_, quit := ev.GetKind().(*pb.Event_Quit)
	s.endTicks(ev.GetTick(), quit)
	s.decideSwings(t, quit)
	s.decideKA(t)
	s.decideBursts(t, ev.GetTick())
	s.decideSnaps(ev.GetTick(), quit)
	if s.vel != nil && t > s.vel.end {
		s.finishVelocity()
	}

	switch k := ev.GetKind().(type) {
	case *pb.Event_Move:
		s.onMove(w, v)
	case *pb.Event_Attack:
		s.onAttack(w, v, ev, k.Attack)
	case *pb.Event_Swing:
		s.onSwing(w, v, ev, k.Swing)
	case *pb.Event_Place:
		s.onPlace(w, v, ev, k.Place)
	case *pb.Event_Dig:
		s.onDig(w, v, ev, k.Dig)
	case *pb.Event_UseItem:
		if ev.GetTick() > 0 {
			s.rec(ev.GetTick()).placeOrUse = true
		}
	case *pb.Event_Click:
		s.onClick(w, ev, t)
	case *pb.Event_Velocity:
		s.onVelocity(w, v, k.Velocity)
	case *pb.Event_Teleport:
		s.afterTeleport = 2
		s.vel = nil // a teleport cancels a pending velocity
	case *pb.Event_Latency:
		s.onLatency(w, v, k.Latency)
	case *pb.Event_Flag:
		s.onFlag(w, k.Flag)
	case *pb.Event_Env:
		s.onEnv(w, k.Env)
	case *pb.Event_CheckStats:
		s.onCheckStats(t, k.CheckStats)
	}
	if !isMove && ev.GetTick() > 0 {
		s.noteTick(ev.GetTick())
	}
	s.onTicked(w, ev, t)
	return out
}

// Housekeep finalises windows whose end is more than 60 s before the partition's newest time.
func (s *Session) Housekeep(partitionNowUs int64) []*Window {
	return s.finaliseWhere(func(w *win) bool { return w.end+HousekeepAfterUs <= partitionNowUs })
}

// Close finalises every open window (session end or replacement by a newer session).
func (s *Session) Close() []*Window {
	return s.finaliseWhere(func(*win) bool { return true })
}

func (s *Session) finaliseWhere(cond func(*win) bool) []*Window {
	var starts []int64
	for start, w := range s.wins {
		if cond(w) {
			starts = append(starts, start)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	var out []*Window
	for _, start := range starts {
		w := s.wins[start]
		delete(s.wins, start)
		s.finalised = max(s.finalised, start)
		if fw := s.build(w); fw != nil {
			out = append(out, fw)
		}
	}
	return out
}

func (s *Session) pruneStalls(t int64) {
	keep := s.stalls[:0]
	for _, st := range s.stalls {
		if st.end >= t-2*WindowUs-HousekeepAfterUs {
			keep = append(keep, st)
		}
	}
	s.stalls = keep
}

func (s *Session) stallOverlaps(a, b int64) bool {
	for _, st := range s.stalls {
		if st.start <= b && a <= st.end {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------
// Event handlers
// ---------------------------------------------------------------------------------------------

func (s *Session) onEnv(w *win, e *pb.EnvSample) {
	special := e.GetFlags()&envSpecialMask != 0
	s.envHave, s.envTickMs, s.envSpecial = true, e.GetServerTickMs(), special
	if w == nil {
		return
	}
	if w.envN == 0 || e.GetServerTickMs() > w.envTickMax {
		w.envTickMax = e.GetServerTickMs()
	}
	w.envN++
	if special {
		w.envSpecial++
	}
}

func (s *Session) onMove(w *win, v *checks.View) {
	m, _ := v.Moves.Last()
	s.moves++
	if w != nil {
		w.moves++
		if m.Focus {
			w.focus++
		}
	}
	afterTeleport := s.afterTeleport > 0
	if afterTeleport {
		s.afterTeleport--
	}
	s.rotationPair(w, v, m, afterTeleport)
	s.velocityMove(v, m)
	if m.Tick > 0 {
		s.noteTick(m.Tick)
		s.inventoryMove(v, m)
	}
}

// rotationPair handles features 18-31 for move m.
func (s *Session) rotationPair(w *win, v *checks.View, m checks.Move, afterTeleport bool) {
	prev, ok := v.Moves.FromEnd(1)
	pair := ok && m.HasRot && m.TicksSinceLast == 1 && m.Focus && !afterTeleport &&
		!m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) && !m.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE)
	if !pair {
		s.prevCombatPair = false
		return
	}
	dyaw := wrap180(m.Rot.Yaw - prev.Rot.Yaw)
	dpitch := m.Rot.Pitch - prev.Rot.Pitch
	since := v.Since(v.LastAttackUs)
	combat, idle := since <= combatPairUs, since > idlePairUs
	if v.LastAttackUs > v.NowUs { // an attack "after" the sample cannot be in [t - x, t]
		combat, idle = false, true
	}
	var cal checks.GCDCalibration
	if s.gcd != nil {
		cal = s.gcd.GCD()
	}
	scoping := m.Has(pb.MoveFlag_MOVE_FLAG_SCOPING)
	axes := [2][2]float64{
		{math.Abs(dyaw), math.Max(math.Abs(m.Rot.Yaw), math.Abs(prev.Rot.Yaw))},
		{math.Abs(dpitch), math.Max(math.Abs(m.Rot.Pitch), math.Abs(prev.Rot.Pitch))},
	}
	if w != nil {
		switch {
		case combat:
			w.cYaw = append(w.cYaw, math.Abs(dyaw))
			w.cPitch = append(w.cPitch, math.Abs(dpitch))
			if s.prevCombatPair {
				w.cAccel = append(w.cAccel, math.Abs(dyaw-s.prevDyaw))
			}
			if cal.Calibrated && !scoping {
				for _, ax := range axes {
					if ax[0] != 0 && ax[0] < 30 {
						w.gcdCN++
						if cal.OnGrid(ax[0], ax[1]) {
							w.gcdCOn++
						}
					}
				}
			}
		case idle:
			w.iYaw = append(w.iYaw, math.Abs(dyaw))
			w.iPitch = append(w.iPitch, math.Abs(dpitch))
			if cal.Calibrated && !scoping && math.Abs(m.Rot.Pitch) < 89 {
				for _, ax := range axes {
					if ax[0] != 0 && ax[0] < 10 {
						w.gcdIN++
						if cal.OnGrid(ax[0], ax[1]) {
							w.gcdION++
						}
					}
				}
			}
		}
	}
	s.prevCombatPair, s.prevDyaw = combat, dyaw
}

func (s *Session) onAttack(w *win, v *checks.View, ev *pb.Event, a *pb.Attack) {
	obs, _ := checks.NewAttackObs(v, ev)
	start := windowStart(v.NowUs)
	if w != nil {
		w.attacks++
		w.targets[a.GetTargetEntityId()] = struct{}{}
	}
	reach := float64(a.GetEntityReach())
	if reach <= 0 {
		reach = 3
	}
	s.attacks.Add(v, atkItem{obs: obs, win: start, reach: reach}, obs.R0)
	// Swings waiting for an attack within 1.5 s after them.
	for i := range s.swings {
		if v.NowUs-s.swings[i].t <= clickAttackUs {
			s.swings[i].near = true
		}
	}
	if t := ev.GetTick(); t > 0 {
		r := s.rec(t)
		if !obs.Stab {
			r.attacks++
			if r.targets == nil {
				r.targets = map[int32]struct{}{}
			}
			r.targets[a.GetTargetEntityId()] = struct{}{}
		}
		if !r.haveAttack {
			r.haveAttack, r.attackWin = true, start
			s.snapQueue = append(s.snapQueue, tickItem{tick: t, win: start})
		}
	}
}

// eligibleAttack applies FEATURES.md "Eligible attack": an attack on a tracked player not skipped by
// CHECKS.md 2.1 (creative/spectator, vehicles, extended reach, stab, ping > 600 ms, 1 s after a
// teleport of either player, server-set rotations).
func eligibleAttack(e checks.R1Entry[atkItem]) bool {
	a := &e.Item.obs
	switch {
	case !a.HasTargetPlayer, a.GameMode == pb.GameMode_GAME_MODE_CREATIVE, a.GameMode == pb.GameMode_GAME_MODE_SPECTATOR,
		a.AttackerInVehicle, a.TargetInVehicle, a.ExtendedReach, a.Stab, a.PingMs > checks.MaxJudgedPingMs,
		a.SinceTeleportUs < checks.TeleportSkipUs, a.TargetTeleported(), a.R0Server, e.R1Server:
		return false
	}
	return true
}

func (s *Session) decideAttack(e checks.R1Entry[atkItem]) {
	w := s.at(e.Item.win)
	if w == nil {
		return
	}
	entry := checks.R1Entry[checks.AttackObs]{Item: e.Item.obs, TimeUs: e.TimeUs, Tick: e.Tick, R0: e.R0, R1: e.R1,
		R1Move: e.R1Move, R1Server: e.R1Server, Move: e.Move}
	if angle, ok := checks.PrecisionAngle(entry); ok {
		w.precision = append(w.precision, angle)
	}
	if !eligibleAttack(e) {
		return
	}
	w.eligible++
	a := &entry.Item
	if dist, _, ok := a.ReachDistance(); ok {
		w.reach = append(w.reach, dist-(e.Item.reach-3))
	}
	boxes := a.TargetBoxes(checks.TargetMargin + checks.HitboxTimingMargin + a.EyeUncertainty())
	if len(boxes) > 0 && !checks.AnyRayHits(a.EyeCandidates(), checks.ActionRotations(e.R0, e.R1), boxes, math.Inf(1)) {
		w.hitboxMiss++
	}
}

func (s *Session) onSwing(w *win, v *checks.View, ev *pb.Event, sw *pb.Swing) {
	tick := ev.GetTick()
	if sw.GetHand() != pb.Hand_HAND_MAIN || tick == 0 {
		return
	}
	s.rec(tick).mainSwing = true
	if w == nil {
		return
	}
	excluded := s.digActive || (s.digEnd != 0 && tick <= s.digEnd+clickDigTicks)
	near := v.LastAttackUs != 0 && v.NowUs-v.LastAttackUs <= clickAttackUs
	s.swings = append(s.swings, swingItem{t: v.NowUs, tick: tick, win: w.start, near: near, excluded: excluded})
}

// decideSwings counts the swings whose tick has ended and whose attack condition is known.
func (s *Session) decideSwings(t int64, quit bool) {
	keep := s.swings[:0]
	for _, sw := range s.swings {
		timeUp := t-sw.t > clickAttackUs
		if quit && !sw.tickDone {
			continue // undecided at the end: excluded
		}
		if !sw.tickDone || (!sw.near && !timeUp && !quit) {
			keep = append(keep, sw)
			continue
		}
		if sw.near && !sw.excluded {
			if w := s.at(sw.win); w != nil {
				w.clicks[sw.tick]++
			}
		}
	}
	s.swings = keep
}

func (s *Session) onPlace(w *win, v *checks.View, ev *pb.Event, p *pb.BlockPlace) {
	if ev.GetTick() > 0 {
		s.rec(ev.GetTick()).placeOrUse = true
	}
	t := v.NowUs
	s.placeTimes = appendRecent(s.placeTimes, t, 1_000_000)
	if w == nil {
		return
	}
	w.places++
	w.placePeak = max(w.placePeak, len(s.placeTimes))
	eye := geom.Eye(v.Pos, v.Pose(), v.Protocol, v.Scale)
	if e := p.GetEye(); e != nil {
		eye = geom.V(e.GetX(), e.GetY(), e.GetZ())
	}
	r0 := geom.Rotation{Yaw: float64(p.GetYaw()), Pitch: float64(p.GetPitch())}
	if p.GetInsideBlock() || !actionJudged(v) {
		return
	}
	s.places.Add(v, placeItem{ev: p, win: w.start, eye: eye}, r0)
}

// actionJudged applies CHECKS.md 2.1 "Skip" to a placement at the view's current state (server-set
// rotations are checked with R0/R1).
func actionJudged(v *checks.View) bool {
	if v.GameMode == pb.GameMode_GAME_MODE_CREATIVE || v.GameMode == pb.GameMode_GAME_MODE_SPECTATOR || v.InVehicle {
		return false
	}
	return v.PingMs <= checks.MaxJudgedPingMs && v.Since(v.LastTeleportUs) >= checks.TeleportSkipUs && !checks.LastRotationFromServer(v)
}

func (s *Session) decidePlace(v *checks.View, e checks.R1Entry[placeItem]) {
	w := s.at(e.Item.win)
	if w == nil || e.R1Server {
		return
	}
	p := e.Item.ev
	b := p.GetBlock()
	bv := geom.V(float64(b.GetX()), float64(b.GetY()), float64(b.GetZ()))
	eye := e.Item.eye
	u := 0.03 + geom.EyeUncertainty(s.info.Protocol)
	w.placeElig++
	if p.GetFace() != pb.Face_FACE_OTHER && !faceVisible(p.GetFace(), eye, bv, u) {
		w.faceInvalid++
	}
	reach := float64(p.GetBlockReach())
	if reach <= 0 {
		reach = 4.5
	}
	rots := checks.ActionRotations(e.R0, e.R1)
	cube := geom.BlockBox(b.GetX(), b.GetY(), b.GetZ())
	if !checks.AnyRayHits([]geom.Vec3{eye}, rots, []geom.AABB{cube.Expand(0.05)}, reach+1.5) {
		w.rayMiss++
	}
	cur := geom.V(float64(p.GetCursorX()), float64(p.GetCursorY()), float64(p.GetCursorZ()))
	if cursorIn01(cur) && s.info.Protocol >= 315 {
		target := bv.Add(cur)
		if target.Sub(eye).Len() >= 0.3 {
			w.cursorN++
			mismatch := true
			for _, r := range rots {
				if a, ok := geom.AngleBetween(r.Look(), target.Sub(eye)); ok && a <= 3 {
					mismatch = false
					break
				}
			}
			if mismatch {
				w.cursorMis++
			}
		}
	}
	w.placeReach = append(w.placeReach, cube.DistanceTo(eye)-(reach-4.5))
}

func cursorIn01(c geom.Vec3) bool {
	in := func(x float64) bool { return x >= 0 && x <= 1 }
	return in(c.X) && in(c.Y) && in(c.Z)
}

// faceVisible is scaffold.face's superset test (CHECKS.md 2.12).
func faceVisible(f pb.Face, e, b geom.Vec3, u float64) bool {
	switch f {
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

func (s *Session) onDig(w *win, v *checks.View, ev *pb.Event, d *pb.BlockDig) {
	switch d.GetAction() {
	case pb.BlockDig_ACTION_START:
		s.digActive = true
		s.digStartTimes = appendRecent(s.digStartTimes, v.NowUs, 1_000_000)
		if w != nil {
			w.digs++
			w.digPeak = max(w.digPeak, len(s.digStartTimes))
		}
	case pb.BlockDig_ACTION_CANCEL, pb.BlockDig_ACTION_FINISH:
		s.digActive = false
		s.digEnd = ev.GetTick()
	default:
		return
	}
	if w == nil || d.GetAction() == pb.BlockDig_ACTION_CANCEL || v.InVehicle ||
		(v.GameMode != pb.GameMode_GAME_MODE_SURVIVAL && v.GameMode != pb.GameMode_GAME_MODE_ADVENTURE) {
		return
	}
	eye := geom.Eye(v.Pos, v.Pose(), v.Protocol, v.Scale)
	if e := d.GetEye(); e != nil {
		eye = geom.V(e.GetX(), e.GetY(), e.GetZ())
	}
	s.digs.Add(v, digItem{ev: d, win: w.start, eye: eye}, geom.Rotation{Yaw: float64(d.GetYaw()), Pitch: float64(d.GetPitch())})
}

func (s *Session) decideDig(e checks.R1Entry[digItem]) {
	w := s.at(e.Item.win)
	if w == nil {
		return
	}
	d := e.Item.ev
	b := d.GetBlock()
	reach := float64(d.GetBlockReach())
	if reach <= 0 {
		reach = 4.5
	}
	w.digElig++
	cube := geom.BlockBox(b.GetX(), b.GetY(), b.GetZ()).Expand(0.05)
	if !checks.AnyRayHits([]geom.Vec3{e.Item.eye}, checks.ActionRotations(e.R0, e.R1), []geom.AABB{cube}, reach+1.5) {
		w.digMiss++
	}
}

func (s *Session) onClick(w *win, ev *pb.Event, t int64) {
	if ev.GetTick() > 0 {
		s.rec(ev.GetTick()).invClick = true
	}
	if w == nil {
		return
	}
	w.invClicks++
	if w.invHaveT {
		d := float64(t-w.lastInvT) / 1000
		if !w.invHave || d < w.invMinMs {
			w.invMinMs, w.invHave = d, true
		}
	}
	w.lastInvT, w.invHaveT = t, true
}

// inventoryMove evaluates inventory.move / inventory.rotate for the tick of move m (CHECKS.md 2.13);
// the tick counts once it ended and both it and the previous tick had a window click.
func (s *Session) inventoryMove(v *checks.View, m checks.Move) {
	r := s.rec(m.Tick)
	r.haveMove, r.moveWin = true, windowStart(v.NowUs)
	prev, ok := v.Moves.FromEnd(1)
	if !ok {
		return
	}
	if m.HasRot && !m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) && !m.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE) && !v.InVehicle {
		if math.Abs(wrap180(m.Rot.Yaw-prev.Rot.Yaw)) > 1 || math.Abs(m.Rot.Pitch-prev.Rot.Pitch) > 1 {
			r.invRot = true
		}
	}
	if !(m.TicksSinceLast == 1 && m.Focus && m.HasPos && prev.HasPos) || m.Has(pb.MoveFlag_MOVE_FLAG_VELOCITY_PENDING) ||
		m.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE) || m.Has(pb.MoveFlag_MOVE_FLAG_GLIDING) || v.InVehicle {
		return
	}
	for _, f := range invEnvExclusions {
		if v.EnvRecently(f, velEnvUs) {
			return
		}
	}
	h := m.Pos.Sub(prev.Pos).HorizontalLen()
	hPrev := 0.0
	if pp, ok := v.Moves.FromEnd(2); ok && prev.HasPos && pp.HasPos {
		hPrev = prev.Pos.Sub(pp.Pos).HorizontalLen()
	}
	if h > 0.1 && h > hPrev*0.91+0.02 {
		r.invMove = true
	}
}

func (s *Session) onVelocity(w *win, v *checks.View, vel *pb.VelocityApplied) {
	if vel.GetSource() == pb.VelocityApplied_SOURCE_EXPLOSION {
		s.vel = nil // no velocity is judged while an explosion arrived within its window
		return
	}
	if s.vel != nil {
		s.vel = nil // a newer velocity replaces the older one (undecided)
	}
	if w == nil || vel.GetVy() <= 0.15 {
		return
	}
	window := int64(rttHigh(v))*1000 + velExtraUs
	if v.LastExplosionUs != 0 && v.NowUs-v.LastExplosionUs <= window {
		return
	}
	for _, f := range velEnvExclusions {
		if v.EnvRecently(f, velEnvUs) {
			return
		}
	}
	gliding := v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_GLIDING) != 0 || v.EnvRecently(pb.EnvFlag_ENV_FLAG_GLIDING, 0)
	flying := v.Flying || v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_FLYING) != 0
	if gliding || flying || v.InVehicle {
		return
	}
	s.vel = &velItem{t: v.NowUs, win: w.start, vx: vel.GetVx(), vy: vel.GetVy(), vz: vel.GetVz(),
		onGroundBefore: vel.GetOnGroundBefore(), end: v.NowUs + window}
}

func rttHigh(v *checks.View) uint32 {
	switch {
	case v.RTTHighMs > 0:
		return max(v.RTTHighMs, v.PingMs)
	case v.PingMs > 0:
		return v.PingMs
	}
	return 100
}

// velocityMove tests move m against the pending velocity (CHECKS.md 2.9, 2.10).
func (s *Session) velocityMove(v *checks.View, m checks.Move) {
	p := s.vel
	if p == nil || v.NowUs > p.end {
		return
	}
	prev, ok := v.Moves.FromEnd(1)
	if !ok || !(m.TicksSinceLast == 1 && m.Focus && m.HasPos && prev.HasPos) {
		return
	}
	dy := m.Pos.Y - prev.Pos.Y
	if !p.haveFirst {
		p.firstDy, p.haveFirst = dy, true
	}
	p.consecutive++
	vy := p.vy
	if math.Abs(vy) < 0.003 {
		vy = 0
	}
	jumpPower := v.JumpStrength
	if amp, ok := v.Effect("minecraft:jump_boost"); ok {
		jumpPower += 0.1 * float64(amp+1)
	}
	plain := math.Abs(dy-vy) < velEps
	jumpReset := prev.OnGround && math.Abs(dy-math.Max(vy, jumpPower)) < velEps
	ceiling := v.EnvRecently(pb.EnvFlag_ENV_FLAG_LOW_CEILING, velEnvUs) && dy <= vy+velEps
	if !plain && !jumpReset && !ceiling {
		return
	}
	s.vel = nil
	w := s.at(p.win)
	if w == nil {
		return
	}
	w.velN++
	if p.haveFirst && p.vy != 0 {
		w.velRatios = append(w.velRatios, p.firstDy/p.vy)
	}
	if p.onGroundBefore {
		w.jrN++
		if !plain && jumpReset {
			w.jrOnly++
		}
	}
	vxz := math.Hypot(p.vx, p.vz)
	if vxz >= 0.3 && !m.HorizontalCollision {
		w.velHN++
		h := m.Pos.Sub(prev.Pos).HorizontalLen()
		w.velHRatios = append(w.velHRatios, h/vxz)
		aMax := 0.026
		if prev.OnGround {
			aMax = v.MovementSpeed * 1.3
		}
		bonus := 0.0
		if prev.OnGround {
			bonus = 0.2
		}
		hMin := math.Max(0, vxz-aMax-bonus-0.003)
		if h < hMin && (hMin-h)/vxz > 0.15 {
			w.velHShort++
		}
	}
}

// finishVelocity decides a pending velocity whose window ended without a match: ignored after >= 3
// consecutive position moves, else undecided (excluded).
func (s *Session) finishVelocity() {
	p := s.vel
	s.vel = nil
	if p == nil || p.consecutive < 3 {
		return
	}
	w := s.at(p.win)
	if w == nil {
		return
	}
	w.velN++
	w.velIgnored++
	if p.haveFirst && p.vy != 0 {
		w.velRatios = append(w.velRatios, p.firstDy/p.vy)
	}
}

func (s *Session) onLatency(w *win, v *checks.View, l *pb.LatencySample) {
	if l.GetUnmatched() {
		return
	}
	rtt := float64(l.GetRttUs()) / 1000
	t := v.NowUs
	switch l.GetKind() {
	case pb.LatencySample_KIND_PONG:
		s.pongs = append(s.pongs, timed{t, rtt})
		keep := s.pongs[:0]
		for _, p := range s.pongs {
			if t-p.t <= 2*kaPairUs {
				keep = append(keep, p)
			}
		}
		s.pongs = keep
		if w != nil {
			w.pongs = append(w.pongs, rtt)
		}
	case pb.LatencySample_KIND_KEEPALIVE:
		if w != nil {
			w.kas = append(w.kas, rtt)
			s.kas = append(s.kas, kaItem{t: t, rtt: rtt, win: w.start})
		}
	}
}

// decideKA pairs keep-alive samples older than 2 s with the median of the pongs within +-2 s.
func (s *Session) decideKA(t int64) {
	keep := s.kas[:0]
	for _, k := range s.kas {
		if t-k.t <= kaPairUs {
			keep = append(keep, k)
			continue
		}
		var near []float64
		for _, p := range s.pongs {
			if p.t >= k.t-kaPairUs && p.t <= k.t+kaPairUs {
				near = append(near, p.x)
			}
		}
		if w := s.at(k.win); w != nil && len(near) > 0 {
			w.kaDeltas = append(w.kaDeltas, k.rtt-fMedian(near))
		}
	}
	s.kas = keep
}

func (s *Session) onFlag(w *win, f *pb.LocalFlag) {
	if w == nil {
		return
	}
	id := f.GetCheckId()
	if i, ok := localFlagFeature[id]; ok && !(id == "protocol.slot" && f.GetVariant() == "redundant") {
		w.flags[i-FFlagTimerBalance]++
	}
	if strings.HasPrefix(id, "movement.") {
		sev := f.GetSeverity()
		if !math.IsNaN(sev) && (!w.moveSevOK || sev > w.moveSev) {
			w.moveSev, w.moveSevOK = sev, true
		}
	}
}

func (s *Session) onCheckStats(t int64, cs *pb.CheckStats) {
	id := cs.GetCheckId()
	list := append(s.checkStats[id], statItem{t: t, s: cs})
	keep := list[:0]
	for _, it := range list {
		if t-it.t <= statsBackUs+WindowUs+GraceUs {
			keep = append(keep, it)
		}
	}
	s.checkStats[id] = keep
}

// onTicked handles every session event with tick > 0: timer offsets and gaps.
func (s *Session) onTicked(w *win, ev *pb.Event, t int64) {
	tick := ev.GetTick()
	if tick == 0 {
		return
	}
	if !s.timerBase {
		s.timerBase, s.baseUs, s.baseTick = true, ev.GetTimeUs(), tick
	}
	if w != nil {
		o := float64(int64(ev.GetTimeUs())-int64(s.baseUs))/1000 - 50*float64(int64(tick)-int64(s.baseTick))
		b := int((t - w.start) / timerBinUs)
		if b >= 0 && b < timerBins {
			if w.timerN[b] == 0 || o < w.timerMin[b] {
				w.timerMin[b] = o
			}
			w.timerN[b]++
		}
	}
	for _, g := range s.bursts {
		if t <= g.tb+burstUs {
			g.lastTick = max(g.lastTick, tick)
		}
	}
	if s.haveTicked && w != nil && t-s.lastTickedT > gapUs && int64(tick)-int64(s.lastTickedTk) <= 2 {
		g := &gapItem{ta: s.lastTickedT, tb: t, tickB: tick, lastTick: tick}
		w.gaps = append(w.gaps, g)
		s.bursts = append(s.bursts, g)
	}
	s.lastTickedT, s.lastTickedTk, s.haveTicked = t, tick, true
}

func (s *Session) decideBursts(t int64, tick uint32) {
	keep := s.bursts[:0]
	for _, g := range s.bursts {
		if t <= g.tb+burstUs {
			keep = append(keep, g)
			continue
		}
		g.decided = true
		g.burst = int64(g.lastTick)-int64(g.tickB) >= burstTicks
	}
	s.bursts = keep
}

// rec returns the record of a client tick.
func (s *Session) rec(tick uint32) *tickRec {
	r := s.ticks[tick]
	if r == nil {
		r = &tickRec{}
		s.ticks[tick] = r
	}
	return r
}

func (s *Session) noteTick(tick uint32) {
	if tick > s.curTick {
		s.curTick = tick
	}
}

// endTicks finishes the ticks before the event's tick (or all at the quit): noswing, multi, clicks'
// place/use exclusion and the inventory ticks.
func (s *Session) endTicks(tick uint32, quit bool) {
	if len(s.ticks) == 0 {
		return
	}
	var done []uint32
	for k := range s.ticks {
		if quit || (tick > 0 && k < tick) {
			done = append(done, k)
		}
	}
	if len(done) == 0 {
		return
	}
	sort.Slice(done, func(i, j int) bool { return done[i] < done[j] })
	for _, k := range done {
		r := s.ticks[k]
		if r.finished {
			continue
		}
		if r.attacks > 0 {
			if w := s.at(r.attackWin); w != nil {
				w.noswingN += r.attacks
				if !r.mainSwing {
					w.noswingMiss += r.attacks
				}
				if len(r.targets) >= 2 {
					w.multiTicks++
				}
			}
		}
		for i := range s.swings {
			if s.swings[i].tick == k {
				s.swings[i].tickDone = true
				s.swings[i].excluded = s.swings[i].excluded || r.placeOrUse
			}
		}
		if r.haveMove && (r.invMove || r.invRot) && r.invClick {
			if p := s.ticks[k-1]; p != nil && p.invClick {
				if w := s.at(r.moveWin); w != nil {
					if r.invMove {
						w.invMove++
					}
					if r.invRot {
						w.invRot++
					}
				}
			}
		}
	}
	// Keep the newest finished tick: the next tick's screen-open test needs its click.
	newest := done[len(done)-1]
	for _, k := range done {
		if k != newest || quit {
			delete(s.ticks, k)
		} else {
			s.ticks[k].finished = true
		}
	}
}

func (s *Session) decideSnaps(tick uint32, quit bool) {
	for len(s.snapQueue) > 0 {
		it := s.snapQueue[0]
		snap, decided := s.snap.Snap(it.tick)
		if !decided && !quit && tick <= it.tick+12 {
			break
		}
		s.snapQueue = s.snapQueue[1:]
		if !decided {
			continue // never decided: excluded
		}
		if w := s.at(it.win); w != nil {
			w.snapTicks++
			if snap {
				w.snaps++
			}
		}
	}
	if len(s.snapQueue) > 0 && s.snapQueue[0].tick > 4 {
		s.snap.Prune(s.snapQueue[0].tick - 4)
	} else if len(s.snapQueue) == 0 && s.curTick > 70 {
		s.snap.Prune(s.curTick - 64)
	}
}

// appendRecent appends t to times and drops entries outside (t - within, t].
func appendRecent(times []int64, t, within int64) []int64 {
	times = append(times, t)
	keep := times[:0]
	for _, x := range times {
		if x > t-within {
			keep = append(keep, x)
		}
	}
	return keep
}

// ---------------------------------------------------------------------------------------------
// Finalisation
// ---------------------------------------------------------------------------------------------

// build turns a finalised accumulator into a Window, or nil when it has no MoveSample.
func (s *Session) build(w *win) *Window {
	if w.moves == 0 {
		return nil
	}
	f := newVec(s.tick)

	// Context 0-5.
	f.setIf(FActiveSeconds, w.nEvents >= 1, float64(w.tLast-w.tFirst)/1e6)
	f.set(FMoveCount, float64(w.moves))
	f.share(FFocusShare, w.focus, w.moves)
	switch {
	case w.envN > 0:
		f.set(FServerTickMsMax, float64(w.envTickMax))
	case w.fbHave:
		f.set(FServerTickMsMax, float64(w.fbTickMs))
	}
	f.set(FServerStallMs, float64(s.stallUnion(w.start, w.end))/1000)
	switch {
	case w.envN > 0:
		f.share(FEnvSpecialShare, w.envSpecial, w.envN)
	case w.fbHave:
		f.set(FEnvSpecialShare, b2f(w.fbSpecial))
	}

	// Clicks 6-17.
	s.buildClicks(f, w)

	// Aim 18-35.
	f.set(FRotCombatCount, float64(len(w.cYaw)))
	if len(w.cYaw) >= 10 {
		f.set(FRotCombatYawMean, fMean(w.cYaw))
		f.set(FRotCombatYawSD, fSD(w.cYaw))
		f.set(FRotCombatPitchMean, fMean(w.cPitch))
		f.set(FRotCombatPitchSD, fSD(w.cPitch))
	}
	f.setIf(FRotCombatYawAccelMean, len(w.cAccel) >= 10, fMean(w.cAccel))
	f.set(FRotIdleCount, float64(len(w.iYaw)))
	if len(w.iYaw) >= 10 {
		f.set(FRotIdleYawMean, fMean(w.iYaw))
		f.set(FRotIdleYawSD, fSD(w.iYaw))
		f.set(FRotIdlePitchMean, fMean(w.iPitch))
		f.set(FRotIdlePitchSD, fSD(w.iPitch))
	}
	if s.gcd != nil && s.gcd.GCD().Calibrated {
		f.set(FGCDCalibrated, 1)
	} else {
		f.set(FGCDCalibrated, 0)
	}
	f.setIf(FGCDCombatOnGridShare, w.gcdCN >= 10, float64(w.gcdCOn)/float64(max(1, w.gcdCN)))
	f.setIf(FGCDIdleOnGridShare, w.gcdIN >= 10, float64(w.gcdION)/float64(max(1, w.gcdIN)))
	f.set(FSnapCount, float64(w.snaps))
	f.share(FSnapShare, w.snaps, w.snapTicks)
	if len(w.precision) >= 3 {
		f.set(FAimPrecisionMean, fMean(w.precision))
		f.set(FAimPrecisionSD, fSD(w.precision))
	}

	// Combat 36-44.
	f.set(FAttackCount, float64(w.attacks))
	f.set(FAttackEligibleCount, float64(w.eligible))
	f.set(FAttackTargetDistinct, float64(len(w.targets)))
	if len(w.reach) >= 1 {
		f.set(FReachP50, fPercentile(w.reach, 50))
		f.set(FReachP95, fPercentile(w.reach, 95))
		f.set(FReachMax, fMax(w.reach))
	}
	f.share(FHitboxMissShare, w.hitboxMiss, w.eligible)
	f.share(FNoSwingShare, w.noswingMiss, w.noswingN)
	f.set(FMultiTargetTicks, float64(w.multiTicks))

	// Velocity 45-51.
	f.set(FVelocityCount, float64(w.velN))
	f.setIf(FVelocityVTakenRatio, len(w.velRatios) >= 1, medianOr(w.velRatios))
	f.share(FVelocityVerticalIgnoredShare, w.velIgnored, w.velN)
	f.setIf(FVelocityJumpResetShare, w.jrN >= 1, float64(w.jrOnly)/float64(max(1, w.jrN)))
	f.set(FVelocityHCount, float64(w.velHN))
	f.setIf(FVelocityHTakenRatio, len(w.velHRatios) >= 1, medianOr(w.velHRatios))
	f.share(FVelocityHShortfallShare, w.velHShort, w.velHN)

	// Movement 52-72.
	for i, n := range w.flags {
		f.set(FFlagTimerBalance+i, float64(n))
	}
	f.setIf(FFlagMovementSeverityMax, w.moveSevOK, w.moveSev)
	s.buildTimer(f, w)
	s.buildCheckStats(f, w)

	// World interaction 73-83.
	f.set(FPlaceCount, float64(w.places))
	f.set(FPlaceEligibleCount, float64(w.placeElig))
	f.share(FPlaceFaceInvalidShare, w.faceInvalid, w.placeElig)
	f.share(FPlaceRayMissShare, w.rayMiss, w.placeElig)
	f.share(FPlaceCursorMismatchShare, w.cursorMis, w.cursorN)
	f.setIf(FPlaceReachMax, len(w.placeReach) >= 1, fMax(w.placeReach))
	f.set(FPlacePeakPerS, float64(w.placePeak))
	f.set(FDigCount, float64(w.digs))
	f.set(FDigEligibleCount, float64(w.digElig))
	f.share(FDigRayMissShare, w.digMiss, w.digElig)
	f.set(FDigPeakPerS, float64(w.digPeak))

	// Inventory 84-87.
	f.set(FInvClickCount, float64(w.invClicks))
	f.setIf(FInvClickIntervalMinMs, w.invHave, w.invMinMs)
	f.set(FInvMoveTicks, float64(w.invMove))
	f.set(FInvRotateTicks, float64(w.invRot))

	// Latency 88-93.
	ping := w.pongs
	if len(ping) == 0 {
		ping = w.kas
	}
	f.setIf(FPingMedianMs, len(ping) >= 1, medianOr(ping))
	f.setIf(FPingJitterMs, len(ping) >= 3, fSDOr(ping))
	f.setIf(FKAPongDeltaMs, len(w.kaDeltas) >= 1, medianOr(w.kaDeltas))
	f.set(FKAPairedCount, float64(len(w.kaDeltas)))
	gaps, bursts := 0, 0
	for _, g := range w.gaps {
		if s.stallOverlaps(g.ta, g.tb) {
			continue
		}
		gaps++
		if g.decided && g.burst {
			bursts++
		}
	}
	f.set(FGapCount, float64(gaps))
	f.set(FGapBurstCount, float64(bursts))

	return &Window{
		TenantID: s.info.TenantID, Player: s.info.Player, ServerID: s.info.ServerID, SessionID: s.info.SessionID,
		WindowStartUs: w.start, WindowEndUs: w.end, ClientProtocol: s.info.Protocol, TickClient: s.tick,
		HotLoaded: s.info.HotLoaded, FeatureVersion: FeatureVersion, Features: f.v, WorkerUnixUs: s.now().UnixMicro(),
	}
}

func (s *Session) buildClicks(f *vec, w *win) {
	ticks := make([]uint32, 0, len(w.clicks))
	total := 0
	for k, c := range w.clicks {
		ticks = append(ticks, k)
		total += c
	}
	sort.Slice(ticks, func(i, j int) bool { return ticks[i] < ticks[j] })
	f.set(FClickCount, float64(total))
	var intervals []float64
	closing, sumI := 0, 0.0
	doubles := 0
	peak := 0
	for i, k := range ticks {
		if w.clicks[k] >= 2 {
			doubles++
		}
		n := 0
		for j := i; j >= 0 && ticks[j]+19 >= k; j-- {
			n += w.clicks[ticks[j]]
		}
		peak = max(peak, n)
		if i == 0 {
			continue
		}
		if d := k - ticks[i-1]; d <= clickMaxInterval {
			intervals = append(intervals, float64(d))
			closing += w.clicks[k]
			sumI += float64(d)
		}
	}
	n := len(intervals)
	f.set(FClickIntervalCount, float64(n))
	f.setIf(FClickCPSMean, n >= 5 && sumI > 0, 20*float64(closing)/sumI)
	f.setIf(FClickCPSPeak, total >= 1, float64(peak))
	if n >= 5 {
		f.set(FClickIntervalMean, fMean(intervals))
		f.set(FClickIntervalSD, fSD(intervals))
		f.set(FClickIntervalDistinct, float64(checks.Distinct(intervals)))
		_, share := checks.ModeShare(intervals)
		f.set(FClickIntervalModeShare, share)
	}
	if n >= 8 {
		if sk, ok := fSkew(intervals); ok {
			f.set(FClickIntervalSkew, sk)
		}
		if ku, ok := fKurtosis(intervals); ok {
			f.set(FClickIntervalKurtosis, ku)
		}
		best := 0.0
		for p := 2; p <= 4; p++ {
			if n-p <= 0 {
				continue
			}
			m := 0
			for j := p; j < n; j++ {
				if intervals[j] == intervals[j-p] {
					m++
				}
			}
			best = math.Max(best, float64(m)/float64(n-p))
		}
		f.set(FClickPatternShare, best)
	}
	f.share(FClickDoublesShare, doubles, len(ticks))
}

// buildTimer fits the slope of the per-bin minimum timer offset (feature 68).
func (s *Session) buildTimer(f *vec, w *win) {
	var xs, ys []float64
	for b := 0; b < timerBins; b++ {
		if w.timerN[b] < 2 {
			continue
		}
		bs := w.start + int64(b)*timerBinUs
		if s.stallOverlaps(bs, bs+timerBinUs-1) {
			continue
		}
		xs = append(xs, float64(2*b+1))
		ys = append(ys, w.timerMin[b])
	}
	if len(xs) < 4 {
		return
	}
	mx, my := fMean(xs), fMean(ys)
	var num, den float64
	for i := range xs {
		num += (xs[i] - mx) * (ys[i] - my)
		den += (xs[i] - mx) * (xs[i] - mx)
	}
	if den > 0 {
		f.set(FTimerOffsetSlope, num/den)
	}
}

var movementStatsChecks = []string{"movement.speed", "movement.fly", "movement.step", "movement.groundspoof", "movement.noslow"}

// latestStats returns the CheckStats of id with the largest t in (end - 330 s, end + G].
func (s *Session) latestStats(id string, end int64) *pb.CheckStats {
	var best *statItem
	for i, it := range s.checkStats[id] {
		if it.t > end-statsBackUs && it.t <= end+GraceUs && (best == nil || it.t >= best.t) {
			best = &s.checkStats[id][i]
		}
	}
	if best == nil {
		return nil
	}
	return best.s
}

func (s *Session) buildCheckStats(f *vec, w *win) {
	var evaluated, candidates, discarded uint64
	found := 0
	for _, id := range movementStatsChecks {
		if cs := s.latestStats(id, w.end); cs != nil {
			found++
			evaluated += uint64(cs.GetEvaluated())
			candidates += uint64(cs.GetCandidates())
			discarded += uint64(cs.GetDiscarded())
		}
	}
	f.setIf(FCSMovementCandidateRate, found >= 1 && evaluated > 0, 1000*float64(candidates)/float64(max(1, evaluated)))
	f.setIf(FCSMovementDiscardShare, candidates > 0, float64(discarded)/float64(max(1, candidates)))
	if cs := s.latestStats("movement.speed", w.end); cs != nil {
		f.set(FCSSpeedMaxStat, cs.GetMaxStat())
	}
	if cs := s.latestStats("movement.fly", w.end); cs != nil {
		f.set(FCSFlyMaxStat, cs.GetMaxStat())
	}
}

// stallUnion returns the length of the union of the stalls intersected with [start, end).
func (s *Session) stallUnion(start, end int64) int64 {
	var spans []span
	for _, st := range s.stalls {
		a, b := max(st.start, start), min(st.end, end)
		if b > a {
			spans = append(spans, span{a, b})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var total, curA, curB int64
	have := false
	for _, sp := range spans {
		if !have || sp.start > curB {
			if have {
				total += curB - curA
			}
			curA, curB, have = sp.start, sp.end, true
		} else if sp.end > curB {
			curB = sp.end
		}
	}
	if have {
		total += curB - curA
	}
	return total
}

func medianOr(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	return fMedian(xs)
}

func fSDOr(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	return fSD(xs)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
