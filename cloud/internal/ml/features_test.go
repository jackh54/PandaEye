package ml

import (
	"math"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// fixture builds a synthetic session of a tick client whose first window starts at T0.
type fixture struct {
	t      *testing.T
	player []byte
	events []*pb.Event
}

const T0 = int64(60_000_000) * WindowUs // a window boundary

func (f *fixture) at(dt time.Duration, tick uint32, kind any) *pb.Event {
	e := &pb.Event{Player: f.player, TimeUs: uint64(T0 + dt.Microseconds()), Tick: tick}
	switch k := kind.(type) {
	case *pb.PlayerJoin:
		e.Kind = &pb.Event_Join{Join: k}
	case *pb.StateChange:
		e.Kind = &pb.Event_State{State: k}
	case *pb.MoveSample:
		e.Kind = &pb.Event_Move{Move: k}
	case *pb.Attack:
		e.Kind = &pb.Event_Attack{Attack: k}
	case *pb.Swing:
		e.Kind = &pb.Event_Swing{Swing: k}
	case *pb.EnvSample:
		e.Kind = &pb.Event_Env{Env: k}
	case *pb.LocalFlag:
		e.Kind = &pb.Event_Flag{Flag: k}
	case *pb.LatencySample:
		e.Kind = &pb.Event_Latency{Latency: k}
	case *pb.InventoryClick:
		e.Kind = &pb.Event_Click{Click: k}
	case *pb.VelocityApplied:
		e.Kind = &pb.Event_Velocity{Velocity: k}
	case *pb.CheckStats:
		e.Kind = &pb.Event_CheckStats{CheckStats: k}
	default:
		f.t.Fatalf("fixture: %T", kind)
	}
	f.events = append(f.events, e)
	return e
}

func tickTime(tick uint32) time.Duration { return time.Duration(tick) * 50 * time.Millisecond }

func run(t *testing.T, events []*pb.Event, gcd checks.GCDSource) []*Window {
	t.Helper()
	v := checks.NewView()
	s := NewSession(SessionInfo{TenantID: "t_aaaaaaaaaaaaaaaaaaaa", ServerID: "s", SessionID: "sess", Protocol: 769}, gcd, nil)
	var out []*Window
	for _, ev := range events {
		v.Apply(ev, 0)
		out = append(out, s.Observe(ev, v)...)
	}
	return append(out, s.Close()...)
}

// TestFeatureFixture runs a scripted 30 s of play and checks a set of features against values
// worked out by hand from ml/FEATURES.md.
func TestFeatureFixture(t *testing.T) {
	f := &fixture{t: t, player: make([]byte, 16)}
	f.player[15] = 1
	target := make([]byte, 16)
	target[15] = 2
	f.at(0, 0, &pb.PlayerJoin{Name: "p", ProtocolVersion: 769, Session: "sess"})
	f.at(time.Millisecond, 0, &pb.StateChange{MovementSpeed: 0.1, JumpStrength: 0.42, Gravity: 0.08, StepHeight: 0.6, Scale: 1, EntityReach: 3, BlockReach: 4.5})
	f.at(10*time.Millisecond, 0, &pb.EnvSample{ServerTickMs: 61})

	pos := geom.V(0, 64, 0)
	yaw := 0.0
	attackTicks := map[uint32]bool{}
	for k := uint32(101); k <= 146; k += 5 {
		attackTicks[k] = true
	}
	for k := uint32(1); k <= 200; k++ {
		if k <= 100 {
			yaw++
		} else {
			yaw += 2
		}
		d := geom.Vec3{}
		switch k {
		case 51:
			d = geom.V(0.3, 0.4, 0) // the knockback, taken in full
		case 52:
			d = geom.V(0, -0.4, 0)
		}
		if attackTicks[k] {
			eye := pos.Add(geom.V(0, 1.62, 0))
			var hist []*pb.TimedPos
			for i := 20; i >= 0; i-- {
				hist = append(hist, &pb.TimedPos{TimeUs: uint64(T0 + (tickTime(k) - time.Duration(i)*50*time.Millisecond).Microseconds()), X: 0, Y: 64, Z: 2.8})
			}
			f.at(tickTime(k), k, &pb.Attack{TargetEntityId: 2000, TargetPlayer: target, Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z},
				Yaw: float32(yaw - 2), TargetWidth: 0.6, TargetHeight: 1.8, TargetHistory: hist, PingMs: 50, RttHighMs: 60,
				EntityReach: 3, EyeHeight: 1.62})
			f.at(tickTime(k), k, &pb.Swing{Hand: pb.Hand_HAND_MAIN})
		}
		pos = pos.Add(d)
		f.at(tickTime(k), k, &pb.MoveSample{X: pos.X, Y: pos.Y, Z: pos.Z, Yaw: float32(yaw), HasPos: true, HasRot: true,
			OnGround: true, TicksSinceLast: 1, PacketsSinceLast: 1, Focus: true})
		switch k {
		case 50:
			f.at(tickTime(k)+time.Millisecond, k, &pb.VelocityApplied{Vx: 0.3, Vy: 0.4, OnGroundBefore: true})
		case 100:
			f.at(tickTime(k)+time.Millisecond, k, &pb.EnvSample{ServerTickMs: 55, Flags: uint32(pb.EnvFlag_ENV_FLAG_IN_WATER)})
		}
	}
	f.at(11*time.Second, 220, &pb.LocalFlag{CheckId: "movement.speed", Severity: 0.3})
	f.at(12*time.Second, 240, &pb.LocalFlag{CheckId: "movement.speed", Severity: 0.6})
	f.at(13*time.Second, 260, &pb.LocalFlag{CheckId: "protocol.slot", Variant: "redundant"})
	f.at(14*time.Second, 280, &pb.LocalFlag{CheckId: "protocol.slot", Variant: "range"})
	f.at(15*time.Second, 300, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 80_000})
	f.at(16*time.Second, 320, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 100_000})
	f.at(16500*time.Millisecond, 330, &pb.LatencySample{Kind: pb.LatencySample_KIND_KEEPALIVE, RttUs: 260_000})
	f.at(17*time.Second, 340, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 120_000})
	f.at(17100*time.Millisecond, 342, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 0, Unmatched: true})
	f.at(20*time.Second, 400, &pb.InventoryClick{WindowId: 0, Slot: 10})
	f.at(20200*time.Millisecond, 404, &pb.InventoryClick{WindowId: 0, Slot: 11})
	f.at(20700*time.Millisecond, 414, &pb.InventoryClick{WindowId: 0, Slot: 12})
	f.at(25*time.Second, 500, &pb.CheckStats{CheckId: "movement.speed", MaxStat: 0.12, Evaluated: 1000, Candidates: 4, Discarded: 1})
	f.at(32*time.Second, 640, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 90_000}) // finalises W

	ws := run(t, f.events, nil)
	if len(ws) != 1 || ws[0].WindowStartUs != T0 || ws[0].WindowEndUs != T0+WindowUs {
		t.Fatalf("windows %+v", ws)
	}
	w := ws[0]
	if w.FeatureVersion != 1 || !w.TickClient || w.ClientProtocol != 769 || w.SessionID != "sess" {
		t.Fatalf("metadata %+v", w)
	}
	idleMean := (99*1 + 14*2) / 113.0
	want := map[int]float64{
		FActiveSeconds:   25,
		FMoveCount:       200,
		FFocusShare:      1,
		FServerTickMsMax: 61,
		FServerStallMs:   0,
		FEnvSpecialShare: 0.5,

		FClickCount: 10, FClickCPSMean: 4, FClickCPSPeak: 4, FClickIntervalCount: 9, FClickIntervalMean: 5,
		FClickIntervalSD: 0, FClickIntervalSkew: 0, FClickIntervalKurtosis: 0, FClickIntervalDistinct: 1,
		FClickIntervalModeShare: 1, FClickPatternShare: 1, FClickDoublesShare: 0,

		FRotCombatCount: 66, FRotCombatYawMean: 2, FRotCombatYawSD: 0, FRotCombatPitchMean: 0, FRotCombatYawAccelMean: 0,
		FRotIdleCount: 113, FRotIdleYawMean: idleMean, FRotIdlePitchMean: 0,
		FGCDCalibrated: 0, FGCDCombatOnGridShare: -1, FGCDIdleOnGridShare: -1,
		FSnapCount: 0, FSnapShare: 0, FAimPrecisionMean: -1, FAimPrecisionSD: -1,

		FAttackCount: 10, FAttackEligibleCount: 10, FAttackTargetDistinct: 1, FReachP50: 2.4, FReachP95: 2.4, FReachMax: 2.4,
		FHitboxMissShare: 1, FNoSwingShare: 0, FMultiTargetTicks: 0,

		FVelocityCount: 1, FVelocityVTakenRatio: 1, FVelocityVerticalIgnoredShare: 0, FVelocityJumpResetShare: 0,
		FVelocityHCount: 1, FVelocityHTakenRatio: 1, FVelocityHShortfallShare: 0,

		FFlagMovementSpeed: 2, FFlagProtocolSlot: 1, FFlagMovementFly: 0, FFlagMovementSeverityMax: 0.6,
		FTimerOffsetSlope:        0,
		FCSMovementCandidateRate: 4, FCSMovementDiscardShare: 0.25, FCSSpeedMaxStat: 0.12, FCSFlyMaxStat: -1,

		FPlaceCount: 0, FPlaceEligibleCount: 0, FPlaceReachMax: -1, FDigCount: 0,
		FInvClickCount: 3, FInvClickIntervalMinMs: 200, FInvMoveTicks: 0,

		FPingMedianMs: 100, FPingJitterMs: math.Sqrt(800.0 / 3), FKAPongDeltaMs: 160, FKAPairedCount: 1,
		FGapCount: 0, FGapBurstCount: 0,
	}
	names := FeatureNames()
	for i, x := range want {
		if got := float64(w.Features[i]); math.Abs(got-x) > 1e-4 {
			t.Errorf("%s = %v, want %v", names[i], got, x)
		}
	}
}

// Defaults, clipping and the tick-only rule.
func TestFeatureDefaultsAndClipping(t *testing.T) {
	f := newVec(false)
	f.set(FMoveCount, 1e9)
	f.set(FReachMax, -3)
	f.set(FClickCount, 12) // tick-only: non-tick clients keep the default
	f.set(FAttackCount, math.NaN())
	if f.v[FMoveCount] != 3000 || f.v[FReachMax] != 0 || f.v[FClickCount] != 0 || f.v[FAttackCount] != 0 {
		t.Fatalf("vec %v %v %v %v", f.v[FMoveCount], f.v[FReachMax], f.v[FClickCount], f.v[FAttackCount])
	}
	d := Defaults()
	if d[FServerTickMsMax] != 50 || d[FClickIntervalKurtosis] != 0 || d[FReachP50] != -1 {
		t.Fatal("defaults")
	}
	if fPercentile([]float64{5, 1, 3, 2, 4}, 95) != 5 || fPercentile([]float64{1, 2, 3, 4}, 50) != 2 || fMedian([]float64{1, 2, 3, 4}) != 2.5 {
		t.Fatal("percentiles")
	}
	if k, ok := fKurtosis([]float64{-1, 1, -1, 1}); !ok || k != 1 {
		t.Fatalf("pearson kurtosis %v", k)
	}
	if wrap180(190) != -170 || wrap180(-190) != 170 {
		t.Fatal("wrap180")
	}
}

// A window without a MoveSample is not emitted; a window is emitted once, finalised by the first
// event at or after its end + G.
func TestFeatureWindowEmission(t *testing.T) {
	f := &fixture{t: t, player: make([]byte, 16)}
	f.at(0, 0, &pb.PlayerJoin{Name: "p", ProtocolVersion: 769, Session: "sess"})
	f.at(time.Second, 20, &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 50_000})
	f.at(31*time.Second, 620, &pb.MoveSample{HasPos: true, OnGround: true, TicksSinceLast: 1, Focus: true})
	f.at(31900*time.Millisecond, 638, &pb.MoveSample{HasPos: true, OnGround: true, TicksSinceLast: 1, Focus: true})
	v := checks.NewView()
	s := NewSession(SessionInfo{Protocol: 769}, nil, nil)
	var got []*Window
	for _, ev := range f.events {
		v.Apply(ev, 0)
		got = append(got, s.Observe(ev, v)...)
	}
	if len(got) != 0 {
		t.Fatalf("window without moves emitted, or the next window too early: %+v", got)
	}
	late := &pb.Event{Player: f.player, TimeUs: uint64(T0 + 62*time.Second.Microseconds()), Tick: 1240,
		Kind: &pb.Event_Move{Move: &pb.MoveSample{HasPos: true, TicksSinceLast: 1, Focus: true}}}
	v.Apply(late, 0)
	got = s.Observe(late, v)
	if len(got) != 1 || got[0].WindowStartUs != T0+WindowUs || got[0].Features[FMoveCount] != 2 {
		t.Fatalf("second window: %+v", got)
	}
	if hk := s.Housekeep(T0 + 200*time.Second.Microseconds()); len(hk) != 1 || hk[0].WindowStartUs != T0+2*WindowUs {
		t.Fatalf("housekeeping: %+v", hk)
	}
	if again := s.Close(); len(again) != 0 {
		t.Fatal("a window is emitted at most once")
	}
}
