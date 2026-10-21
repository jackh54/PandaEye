package checks

import (
	"math"
	"testing"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func TestRing(t *testing.T) {
	r := NewRing[int](3)
	if _, ok := r.Last(); ok {
		t.Fatal("empty ring has a last element")
	}
	for i := 1; i <= 5; i++ {
		r.Push(i)
	}
	if got := r.Slice(); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("slice = %v, want [3 4 5]", got)
	}
	if v, _ := r.FromEnd(1); v != 4 {
		t.Fatalf("FromEnd(1) = %d, want 4", v)
	}
	r.Reset()
	if r.Len() != 0 {
		t.Fatal("reset ring not empty")
	}
}

func TestStats(t *testing.T) {
	xs := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	if m := Mean(xs); m != 5 {
		t.Fatalf("mean = %v", m)
	}
	if sd := StdDev(xs); math.Abs(sd-2.138) > 1e-3 {
		t.Fatalf("sd = %v", sd)
	}
	if p := Percentile(xs, 50); p != 4.5 {
		t.Fatalf("median = %v", p)
	}
	if mode, share := ModeShare(xs); mode != 4 || share != 3.0/8 {
		t.Fatalf("mode = %v share = %v", mode, share)
	}
	if Distinct(xs) != 5 {
		t.Fatal("distinct")
	}
	if Skewness([]float64{1, 1, 1}) != 0 || ExcessKurtosis([]float64{3, 3, 3, 3}) != 0 {
		t.Fatal("degenerate moments must be 0")
	}
	if k := ExcessKurtosis([]float64{-1, 1, -1, 1, -1, 1}); math.Abs(k+2) > 1e-9 {
		t.Fatalf("two-point kurtosis = %v, want -2", k)
	}
}

func move(tick uint32, x, y, z float64, ground bool) *pb.Event {
	return &pb.Event{TimeUs: uint64(tick) * 50_000, Tick: tick, Kind: &pb.Event_Move{Move: &pb.MoveSample{
		X: x, Y: y, Z: z, HasPos: true, OnGround: ground, TicksSinceLast: 1, PacketsSinceLast: 1, Focus: true}}}
}

func TestViewApply(t *testing.T) {
	v := NewView()
	v.Apply(&pb.Event{TimeUs: 1, Kind: &pb.Event_Join{Join: &pb.PlayerJoin{Name: "a", ProtocolVersion: 774, Session: "s1"}}}, 0)
	if !v.TickClient || !v.Online || v.SessionID != "s1" || !v.Judgeable() {
		t.Fatalf("join not applied: %+v", v)
	}
	v.Apply(move(10, 1, 64, 1, true), 0)
	rot := &pb.Event{TimeUs: 11 * 50_000, Tick: 11, Kind: &pb.Event_Move{Move: &pb.MoveSample{Yaw: 90, Pitch: 10, HasRot: true, TicksSinceLast: 1, PacketsSinceLast: 1}}}
	v.Apply(rot, 0)
	last, _ := v.Moves.Last()
	if last.Pos.X != 1 || last.Rot.Yaw != 90 || last.HasPos {
		t.Fatalf("rotation-only move must carry the position forward: %+v", last)
	}
	v.Apply(move(12, 1.2, 64, 1, true), 0)
	prev, _ := v.Moves.FromEnd(1)
	cur, _ := v.Moves.Last()
	if Consecutive(prev, cur, true) {
		t.Fatal("a move after a rotation-only packet has no position predecessor")
	}
	v.Apply(move(13, 1.4, 64, 1, true), 0)
	prev, _ = v.Moves.FromEnd(1)
	cur, _ = v.Moves.Last()
	if !Consecutive(prev, cur, true) {
		t.Fatal("ticks 12 -> 13 with positions are consecutive")
	}
	v.Apply(&pb.Event{TimeUs: 14 * 50_000, Kind: &pb.Event_Env{Env: &pb.EnvSample{Flags: uint32(pb.EnvFlag_ENV_FLAG_IN_WATER), ServerTickMs: 61}}}, 0)
	v.Apply(&pb.Event{TimeUs: 20 * 50_000, Kind: &pb.Event_Env{Env: &pb.EnvSample{}}}, 0)
	if !v.EnvRecently(pb.EnvFlag_ENV_FLAG_IN_WATER, 400_000) || v.EnvRecently(pb.EnvFlag_ENV_FLAG_IN_WATER, 200_000) {
		t.Fatal("EnvRecently window wrong")
	}
	if v.ServerTickMs != 0 && v.ServerTickMs != 61 {
		t.Fatalf("server tick = %v", v.ServerTickMs)
	}
	v.Apply(&pb.Event{TimeUs: 21 * 50_000, Kind: &pb.Event_State{State: &pb.StateChange{GameMode: pb.GameMode_GAME_MODE_CREATIVE}}}, 0)
	if v.Judgeable() || v.MovementSpeed != 0.1 {
		t.Fatal("creative players are not judgeable; missing attributes default")
	}
	v.Apply(&pb.Event{TimeUs: 22 * 50_000, Kind: &pb.Event_Latency{Latency: &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 80_000}}}, 0)
	v.Apply(&pb.Event{TimeUs: 23 * 50_000, Kind: &pb.Event_Latency{Latency: &pb.LatencySample{Kind: pb.LatencySample_KIND_PONG, RttUs: 120_000}}}, 0)
	if v.PingMs != 100 || v.RTTHighMs != 120 {
		t.Fatalf("ping %d high %d", v.PingMs, v.RTTHighMs)
	}
	v.Apply(&pb.Event{TimeUs: 30 * 50_000, Kind: &pb.Event_Join{Join: &pb.PlayerJoin{Update: true, Exempt: true}}}, 0)
	if v.SessionID != "s1" || v.Moves.Len() == 0 || !v.Exempt {
		t.Fatal("an update join must not reset the session but must apply exempt")
	}
}

type recordDetector struct{}

func (recordDetector) Handle(c *Ctx, ev *pb.Event) {
	if _, ok := ev.GetKind().(*pb.Event_Attack); ok {
		c.Emit(Emission{Check: "reach.distance", Points: 5, Confidence: 2})
	}
}

func TestCtxEmit(t *testing.T) {
	v := NewView()
	v.Online = true
	var got []Emission
	c := NewCtx(v, nil, func(e Emission) { got = append(got, e) })
	ev := &pb.Event{TimeUs: 42, Kind: &pb.Event_Attack{Attack: &pb.Attack{}}}
	c.SetEvent(ev)
	recordDetector{}.Handle(c, ev)
	if len(got) != 1 || got[0].Confidence != 1 || got[0].EventTimeUs != 42 {
		t.Fatalf("emission = %+v", got)
	}
	v.Exempt = true
	recordDetector{}.Handle(c, ev)
	c.Silent = true
	v.Exempt = false
	recordDetector{}.Handle(c, ev)
	if len(got) != 1 {
		t.Fatal("exempt players and silent contexts must not emit")
	}
}

func TestRegisterRejectsUnknownChecks(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a local check id must panic")
		}
	}()
	Register(Factory{Name: "bad-local", Checks: []string{"movement.speed"}, New: func() Detector { return recordDetector{} }})
}
