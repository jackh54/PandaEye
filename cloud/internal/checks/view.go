package checks

import (
	"math/bits"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// View is the derived state of one player that every Detector reads. The worker owns it and calls Apply
// for each event before dispatching the event to the detectors. Times ending in Us are normalised cloud
// times (plugin event time + the connection's clock offset); 0 means "never".
type View struct {
	Player    [16]byte
	Name      string
	SessionID string
	ServerID  string

	Protocol   int32 // client protocol; -1 unknown
	TickClient bool  // protocol >= 768: CLIENT_TICK_END native, at most one movement packet per tick
	Bedrock    bool
	Exempt     bool
	HotLoaded  bool
	Online     bool

	GameMode      pb.GameMode
	InVehicle     bool
	Flying        bool
	AllowFlight   bool
	Dead          bool
	Scale         float64
	MovementSpeed float64
	JumpStrength  float64
	Gravity       float64
	StepHeight    float64
	EntityReach   float64
	BlockReach    float64
	Effects       map[string]int32 // effect key ("minecraft:speed") -> amplifier

	// The event being handled.
	NowUs int64
	Tick  uint32

	// Movement (carried forward: a rotation-only packet keeps the last position and vice versa).
	Pos      geom.Vec3
	Rot      geom.Rotation
	OnGround bool
	Flags    uint32 // MoveFlag bits of the newest move
	Moves    Ring[Move]

	// Environment from the plugin's sampler.
	Env          uint32 // EnvFlag bits of the newest sample
	EnvSetAtUs   [32]int64
	ServerTickMs float64

	// Timestamps of notable events.
	LastTeleportUs       int64
	LastServerRotationUs int64
	LastVelocityUs       int64 // velocity packet
	LastExplosionUs      int64
	LastAttackUs         int64 // this player attacked
	LastPlaceUs          int64
	LastDigUs            int64
	LastClickUs          int64
	JoinedUs             int64

	// Latency.
	PingMs    uint32 // best current RTT estimate (from the newest Attack, else the pong/keep-alive median)
	RTTHighMs uint32
	Latency   Ring[LatencyObs]

	// Server stalls reported for this player's server (fanned out to every partition by the gateway).
	Stalls Ring[Stall]
}

// Move is one movement packet as seen by the cloud.
type Move struct {
	TimeUs              int64
	Tick                uint32
	Pos                 geom.Vec3
	Rot                 geom.Rotation
	OnGround            bool
	HasPos, HasRot      bool
	Flags               uint32
	TicksSinceLast      uint32
	PacketsSinceLast    uint32
	Focus               bool
	HorizontalCollision bool
}

// Has reports whether the move carries a MoveFlag.
func (m Move) Has(f pb.MoveFlag) bool { return m.Flags&uint32(f) != 0 }

// LatencyObs is one RTT observation.
type LatencyObs struct {
	TimeUs    int64
	Kind      pb.LatencySample_Kind
	RTTUs     uint32
	Unmatched bool
}

// Stall is a server-side stall window (normalised times).
type Stall struct {
	StartUs, EndUs int64
	Share          float32
}

// Ring sizes.
const (
	MovesKept   = 128 // >= 6 s of full-rate movement
	LatencyKept = 64
	StallsKept  = 16
)

// NewView returns an empty view with vanilla defaults.
func NewView() *View {
	v := &View{
		Moves:   NewRing[Move](MovesKept),
		Latency: NewRing[LatencyObs](LatencyKept),
		Stalls:  NewRing[Stall](StallsKept),
	}
	v.resetSession()
	return v
}

func (v *View) resetSession() {
	v.GameMode = pb.GameMode_GAME_MODE_SURVIVAL
	v.InVehicle, v.Flying, v.AllowFlight, v.Dead = false, false, false, false
	v.Scale, v.MovementSpeed, v.JumpStrength, v.Gravity, v.StepHeight = 1, 0.1, 0.42, 0.08, 0.6
	v.EntityReach, v.BlockReach = 3, 4.5
	v.Effects = map[string]int32{}
	v.Pos, v.Rot, v.OnGround, v.Flags = geom.Vec3{}, geom.Rotation{}, false, 0
	v.Moves.Reset()
	v.Env, v.EnvSetAtUs, v.ServerTickMs = 0, [32]int64{}, 50
	v.LastTeleportUs, v.LastServerRotationUs, v.LastVelocityUs, v.LastExplosionUs = 0, 0, 0, 0
	v.LastAttackUs, v.LastPlaceUs, v.LastDigUs, v.LastClickUs = 0, 0, 0, 0
	v.PingMs, v.RTTHighMs = 0, 0
	v.Latency.Reset()
}

// Apply updates the view for ev. offsetUs converts the plugin's event time to cloud time.
func (v *View) Apply(ev *pb.Event, offsetUs int64) {
	v.NowUs = int64(ev.GetTimeUs()) + offsetUs
	v.Tick = ev.GetTick()
	switch k := ev.GetKind().(type) {
	case *pb.Event_Join:
		j := k.Join
		if !j.GetUpdate() {
			v.resetSession()
			v.SessionID = j.GetSession()
			v.Name = j.GetName()
			v.Protocol = j.GetProtocolVersion()
			v.TickClient = v.Protocol >= geom.Protocol1_21_2
			v.HotLoaded = j.GetHotLoaded()
			v.GameMode = j.GetGameMode()
			v.JoinedUs = v.NowUs
		} else if j.GetSession() != "" {
			v.SessionID = j.GetSession()
		}
		if j.GetName() != "" {
			v.Name = j.GetName()
		}
		v.Bedrock, v.Exempt, v.Online = j.GetBedrock(), j.GetExempt(), true
	case *pb.Event_Quit:
		v.Online = false
	case *pb.Event_State:
		s := k.State
		v.GameMode, v.Flying, v.AllowFlight, v.InVehicle = s.GetGameMode(), s.GetFlying(), s.GetAllowFlight(), s.GetInVehicle()
		v.MovementSpeed = positiveOr(s.GetMovementSpeed(), 0.1)
		v.JumpStrength = positiveOr(s.GetJumpStrength(), 0.42)
		v.Gravity = positiveOr(s.GetGravity(), 0.08)
		v.StepHeight = positiveOr(s.GetStepHeight(), 0.6)
		v.Scale = positiveOr(s.GetScale(), 1)
		v.EntityReach = positiveOr(s.GetEntityReach(), 3)
		v.BlockReach = positiveOr(s.GetBlockReach(), 4.5)
		v.Effects = make(map[string]int32, len(s.GetEffects()))
		for _, e := range s.GetEffects() {
			v.Effects[e.GetEffect()] = e.GetAmplifier()
		}
		v.Exempt, v.Bedrock = s.GetExempt(), s.GetBedrock()
	case *pb.Event_Move:
		m := k.Move
		mv := Move{
			TimeUs: v.NowUs, Tick: v.Tick, OnGround: m.GetOnGround(), HasPos: m.GetHasPos(), HasRot: m.GetHasRot(),
			Flags: m.GetFlags(), TicksSinceLast: m.GetTicksSinceLast(), PacketsSinceLast: m.GetPacketsSinceLast(),
			Focus: m.GetFocus(), HorizontalCollision: m.GetHorizontalCollision(),
			Pos: v.Pos, Rot: v.Rot,
		}
		if mv.HasPos {
			mv.Pos = geom.V(m.GetX(), m.GetY(), m.GetZ())
		}
		if mv.HasRot {
			mv.Rot = geom.Rotation{Yaw: float64(m.GetYaw()), Pitch: float64(m.GetPitch())}
		}
		v.Pos, v.Rot, v.OnGround, v.Flags = mv.Pos, mv.Rot, mv.OnGround, mv.Flags
		v.InVehicle = mv.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE) || v.InVehicle && !mv.HasPos
		v.Dead = mv.Has(pb.MoveFlag_MOVE_FLAG_DEAD)
		if mv.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) {
			v.LastServerRotationUs = v.NowUs
		}
		v.Moves.Push(mv)
	case *pb.Event_Teleport:
		v.LastTeleportUs = v.NowUs
	case *pb.Event_Velocity:
		if k.Velocity.GetSource() == pb.VelocityApplied_SOURCE_EXPLOSION {
			v.LastExplosionUs = v.NowUs
		} else {
			v.LastVelocityUs = v.NowUs
		}
	case *pb.Event_Attack:
		a := k.Attack
		v.LastAttackUs = v.NowUs
		if a.GetPingMs() > 0 {
			v.PingMs = a.GetPingMs()
		}
		if a.GetRttHighMs() > 0 {
			v.RTTHighMs = a.GetRttHighMs()
		}
	case *pb.Event_Place:
		v.LastPlaceUs = v.NowUs
	case *pb.Event_Dig:
		v.LastDigUs = v.NowUs
	case *pb.Event_Click:
		v.LastClickUs = v.NowUs
	case *pb.Event_Latency:
		l := k.Latency
		v.Latency.Push(LatencyObs{TimeUs: v.NowUs, Kind: l.GetKind(), RTTUs: l.GetRttUs(), Unmatched: l.GetUnmatched()})
		v.updatePing()
	case *pb.Event_Env:
		e := k.Env
		// Samples are sent on change (and at least every 5 s), so a flag that was set in the previous
		// sample and is clear now was true until this moment: record that as its last-set time too.
		for f := e.GetFlags() | v.Env; f != 0; f &= f - 1 {
			v.EnvSetAtUs[bits.TrailingZeros32(f)] = v.NowUs
		}
		v.Env = e.GetFlags()
		if e.GetServerTickMs() > 0 {
			v.ServerTickMs = float64(e.GetServerTickMs())
		}
	case *pb.Event_Stall:
		s := k.Stall
		v.Stalls.Push(Stall{StartUs: int64(s.GetStartUs()) + offsetUs, EndUs: int64(s.GetEndUs()) + offsetUs, Share: s.GetStalledShare()})
	}
}

// updatePing recomputes PingMs (median of recent pongs, else the newest keep-alive) and RTTHighMs.
func (v *View) updatePing() {
	var pongs []float64
	var lastKA uint32
	var high uint32
	n := 0
	for i := v.Latency.Len() - 1; i >= 0 && n < 5; i-- {
		o := v.Latency.At(i)
		if o.Unmatched || o.RTTUs == 0 || v.NowUs-o.TimeUs > 30_000_000 {
			continue
		}
		ms := o.RTTUs / 1000
		if ms > high {
			high = ms
		}
		if o.Kind == pb.LatencySample_KIND_PONG {
			pongs = append(pongs, float64(ms))
		} else if lastKA == 0 {
			lastKA = ms
		}
		n++
	}
	switch {
	case len(pongs) > 0:
		v.PingMs = uint32(Median(pongs))
	case lastKA > 0:
		v.PingMs = lastKA
	}
	if high > 0 {
		v.RTTHighMs = high
	}
}

// EnvRecently reports whether the env sampler saw flag within the last withinUs.
func (v *View) EnvRecently(flag pb.EnvFlag, withinUs int64) bool {
	if uint32(flag) == 0 {
		return false
	}
	if v.Env&uint32(flag) != 0 {
		return true
	}
	at := v.EnvSetAtUs[bits.TrailingZeros32(uint32(flag))]
	return at != 0 && v.NowUs-at <= withinUs
}

// Effect returns the amplifier of an active effect.
func (v *View) Effect(key string) (amplifier int32, ok bool) {
	a, ok := v.Effects[key]
	return a, ok
}

// Pose derives the eye pose from the newest move flags.
func (v *View) Pose() geom.Pose {
	switch {
	case v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_GLIDING) != 0:
		return geom.PoseGliding
	case v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_SWIMMING) != 0:
		return geom.PoseSwimming
	case v.Flags&uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING) != 0:
		return geom.PoseSneaking
	}
	return geom.PoseStanding
}

// Judgeable reports whether cloud checks may judge this player right now: online, not bedrock or
// exempt, survival or adventure, alive and not in a vehicle.
func (v *View) Judgeable() bool {
	if !v.Online || v.Bedrock || v.Exempt || v.Dead || v.InVehicle {
		return false
	}
	return v.GameMode == pb.GameMode_GAME_MODE_SURVIVAL || v.GameMode == pb.GameMode_GAME_MODE_ADVENTURE
}

// Since returns NowUs - t, or a very large value when t is 0 ("never").
func (v *View) Since(t int64) int64 {
	if t == 0 {
		return 1 << 62
	}
	return v.NowUs - t
}

// InStall reports whether [startUs, endUs] overlaps a reported server stall.
func (v *View) InStall(startUs, endUs int64) bool {
	for i := 0; i < v.Stalls.Len(); i++ {
		s := v.Stalls.At(i)
		if s.StartUs <= endUs && startUs <= s.EndUs {
			return true
		}
	}
	return false
}

// Consecutive reports whether m directly follows prev with exactly one client tick and both carry a
// position (CHECKS.md section 0). Non-tick clients: no other movement packet in between.
func Consecutive(prev, m Move, tickClient bool) bool {
	if !prev.HasPos || !m.HasPos {
		return false
	}
	if tickClient {
		return m.Tick != 0 && m.Tick == prev.Tick+1 && m.TicksSinceLast <= 1
	}
	return m.PacketsSinceLast == 1 && m.TicksSinceLast <= 1
}

func positiveOr(x, def float64) float64 {
	if x > 0 {
		return x
	}
	return def
}
