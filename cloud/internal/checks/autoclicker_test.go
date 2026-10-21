package checks_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// clickPlan describes what happens in one client tick of a click scenario.
type clickPlan struct {
	swings int
	attack bool
	place  bool
	use    bool
	dig    pb.BlockDig_Action
	digSet bool
	off    bool // off-hand swings instead of main hand
}

// playTicks sends one tick per plan entry: the attack first (vanilla sends INTERACT_ENTITY before the
// swing), then digs/placements/use items, the swings, and the tick's movement packet.
func playTicks(r *rig, plans []clickPlan) {
	for _, p := range plans {
		if p.attack {
			r.attack()
		}
		if p.digSet {
			r.emit(func(e *pb.Event) {
				e.Kind = &pb.Event_Dig{Dig: &pb.BlockDig{Action: p.dig, Block: &pb.Vec3I{X: 0, Y: 64, Z: 2},
					Face: pb.Face_FACE_NORTH, Eye: &pb.Vec3{X: r.p.Pos.X, Y: r.p.Pos.Y + 1.62, Z: r.p.Pos.Z}, BlockReach: 4.5}}
			})
		}
		if p.place {
			r.emit(func(e *pb.Event) {
				e.Kind = &pb.Event_Place{Place: &pb.BlockPlace{Block: &pb.Vec3I{X: 0, Y: 63, Z: 1}, Face: pb.Face_FACE_UP,
					CursorX: 0.5, CursorY: 1, CursorZ: 0.5, Eye: &pb.Vec3{X: r.p.Pos.X, Y: r.p.Pos.Y + 1.62, Z: r.p.Pos.Z}, BlockReach: 4.5}}
			})
		}
		if p.use {
			r.useItem()
		}
		for range p.swings {
			if p.off {
				r.swing(pb.Hand_HAND_OFF)
			} else {
				r.swing(pb.Hand_HAND_MAIN)
			}
		}
		r.step(geom.V(0.05, 0, 0.05))
	}
	r.step(geom.V(0.05, 0, 0.05)) // a later tick decides the last one
}

// clickTimes converts click times (ms from the start) into per-tick plans; every attackEvery-th click is
// also an attack (0 = no attacks).
func clickTimes(ms []float64, attackEvery int) []clickPlan {
	if len(ms) == 0 {
		return nil
	}
	plans := make([]clickPlan, int(ms[len(ms)-1]/50)+1)
	for i, t := range ms {
		k := int(t / 50)
		plans[k].swings++
		if attackEvery > 0 && i%attackEvery == 0 {
			plans[k].attack = true
		}
	}
	return plans
}

// periodic returns click times following the repeating interval pattern (ticks) for n clicks.
func periodic(pattern []int, n int) []float64 {
	out := make([]float64, n)
	t := 10.0
	for i := range out {
		out[i] = t
		t += float64(pattern[i%len(pattern)]) * 50
	}
	return out
}

// human returns n click times with normally distributed intervals (ms).
func human(rng *rand.Rand, meanMs, sdMs float64, n int) []float64 {
	out := make([]float64, n)
	t := rng.Float64() * 50
	for i := range out {
		out[i] = t
		t += math.Max(15, meanMs+rng.NormFloat64()*sdMs)
	}
	return out
}

var cpsOn = rigConfig{enabled: map[string]bool{"autoclicker.cps": true}}

func TestAutoclickerCheatConsistent(t *testing.T) {
	r := newRig(t, rigProtocol)
	playTicks(r, clickTimes(periodic([]int{2}, 200), 4)) // 10 CPS, every interval exactly 2 ticks
	es := r.run("autoclicker")
	expectCount(t, es, "autoclicker.consistency", 5) // 200 clicks = 5 windows
	expectCount(t, es, "autoclicker.pattern", 0)
	expectCount(t, es, "autoclicker.cps", 0)
	expectScoring(t, es, "autoclicker.consistency", 4, 4, 0.7)
}

func TestAutoclickerCheatPattern(t *testing.T) {
	for _, pattern := range [][]int{{1, 2}, {1, 2, 2}, {2, 1, 2, 3}} {
		r := newRig(t, rigProtocol)
		playTicks(r, clickTimes(periodic(pattern, 200), 3))
		es := r.run("autoclicker")
		expectCount(t, es, "autoclicker.pattern", 5)
		expectCount(t, es, "autoclicker.consistency", 0)
		expectScoring(t, es, "autoclicker.pattern", 4, 4, 0.7)
	}
	// Alternating 2- and 3-tick intervals is 8 CPS, on the band's edge: windows with one more 3 than 2
	// are 7.96 CPS. Some windows still emit.
	r := newRig(t, rigProtocol)
	playTicks(r, clickTimes(periodic([]int{2, 3}, 400), 3))
	expectAtLeast(t, r.run("autoclicker"), "autoclicker.pattern", 3)
}

func TestAutoclickerLegitHuman(t *testing.T) {
	for seed := range uint64(20) {
		rng := rand.New(rand.NewPCG(seed, 7))
		r := newRig(t, rigProtocol)
		mean := 70 + rng.Float64()*80 // 6.7 .. 14 CPS
		sd := 12 + rng.Float64()*25
		playTicks(r, clickTimes(human(rng, mean, sd, 400), 3))
		es := r.runWith("autoclicker", cpsOn)
		if len(es) != 0 {
			t.Fatalf("seed %d (mean %.0f ms, sd %.0f ms): %+v", seed, mean, sd, es)
		}
	}
}

func TestAutoclickerLegitDragClicking25CPS(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	r := newRig(t, rigProtocol)
	playTicks(r, clickTimes(human(rng, 40, 14, 600), 5)) // ~25 CPS: 0..3 clicks per tick
	es := r.run("autoclicker")
	if len(es) != 0 {
		t.Fatalf("drag clicking must not emit with the default config: %+v", es)
	}
	// autoclicker.cps is a server policy (disabled by default). At limit 25 a drag clicker at ~25 CPS
	// only flags when two consecutive windows are both >= 25.
	for _, e := range r.runWith("autoclicker", cpsOn) {
		if e.Check != "autoclicker.cps" {
			t.Fatalf("only autoclicker.cps may emit for a drag clicker: %+v", e)
		}
	}
}

func TestAutoclickerCheatCPS(t *testing.T) {
	r := newRig(t, rigProtocol)
	plans := clickTimes(periodic([]int{1, 1, 0}, 400), 6) // 3 clicks every 2 ticks = 30 CPS
	playTicks(r, plans)
	expectCount(t, r.run("autoclicker"), "autoclicker.cps", 0) // disabled by default
	es := r.runWith("autoclicker", cpsOn)
	expectCount(t, es, "autoclicker.cps", 9) // 10 windows, every one after the first
	expectScoring(t, es, "autoclicker.cps", 2, 8, 0.6)
	for _, e := range es {
		if want := 2 + math.Min(6, (e.Data["cps"]-25)*0.5); math.Abs(e.Points-want) > 1e-9 {
			t.Fatalf("points %v, want %v", e.Points, want)
		}
	}
	strict := rigConfig{enabled: cpsOn.enabled, params: map[string]float64{"autoclicker.cps/limit": 31}}
	expectCount(t, r.runWith("autoclicker", strict), "autoclicker.cps", 0)
}

func TestAutoclickerLegitMiningAfterCombat(t *testing.T) {
	// Fighting a mob while mining obsidian and cobwebs: every attack aborts the dig, then the player
	// resumes it; vanilla swings every tick while digging. With the dig exclusion only attack swings are
	// clicks; without it the same swings are 20 CPS.
	build := func(withDigs bool) []clickPlan {
		var plans []clickPlan
		for cycle := range 20 {
			plans = append(plans, clickPlan{attack: true, swings: 1})
			start := clickPlan{swings: 1, dig: pb.BlockDig_ACTION_START, digSet: withDigs}
			plans = append(plans, start)
			for range 14 {
				plans = append(plans, clickPlan{swings: 1})
			}
			end := pb.BlockDig_ACTION_CANCEL
			if cycle%4 == 3 {
				end = pb.BlockDig_ACTION_FINISH // a cobweb breaks
			}
			plans = append(plans, clickPlan{swings: 1, dig: end, digSet: withDigs})
		}
		return plans
	}
	strict := rigConfig{enabled: cpsOn.enabled, params: map[string]float64{"autoclicker.cps/limit": 15}}
	r := newRig(t, rigProtocol)
	playTicks(r, build(true))
	if es := r.runWith("autoclicker", strict); len(es) != 0 {
		t.Fatalf("mining swings counted as clicks: %+v", es)
	}
	control := newRig(t, rigProtocol)
	playTicks(control, build(false))
	expectAtLeast(t, control.runWith("autoclicker", strict), "autoclicker.cps", 1)
}

func TestAutoclickerInstantBreakThenAutoclick(t *testing.T) {
	// A block broken instantly gets START only; the following attack ends the dig exclusion, so an
	// autoclicker cannot hide behind one.
	r := newRig(t, rigProtocol)
	playTicks(r, []clickPlan{{swings: 1, dig: pb.BlockDig_ACTION_START, digSet: true}})
	playTicks(r, clickTimes(periodic([]int{2}, 120), 4))
	expectAtLeast(t, r.run("autoclicker"), "autoclicker.consistency", 2)
}

func TestAutoclickerExcludesPlacementAndUseTicks(t *testing.T) {
	build := func(mark bool) []clickPlan {
		plans := clickTimes(periodic([]int{2}, 200), 5)
		for i := range plans {
			if plans[i].swings > 0 && !plans[i].attack && mark {
				if i%4 == 0 {
					plans[i].place = true // right-click placing: the swing comes with the placement
				} else {
					plans[i].use = true
				}
			}
		}
		return plans
	}
	r := newRig(t, rigProtocol)
	playTicks(r, build(true))
	if es := r.run("autoclicker"); len(es) != 0 {
		t.Fatalf("placement/use swings counted as clicks: %+v", es)
	}
	control := newRig(t, rigProtocol)
	playTicks(control, build(false))
	expectAtLeast(t, control.run("autoclicker"), "autoclicker.consistency", 1)
}

func TestAutoclickerNotJudged(t *testing.T) {
	plans := clickTimes(periodic([]int{2}, 200), 4)
	t.Run("off-hand", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		off := append([]clickPlan(nil), plans...)
		for i := range off {
			off[i].off = true
		}
		playTicks(r, off)
		expectCount(t, r.run("autoclicker"), "autoclicker.consistency", 0)
	})
	t.Run("legacy client", func(t *testing.T) {
		r := newRig(t, 340)
		playTicks(r, plans)
		expectCount(t, r.run("autoclicker"), "autoclicker.consistency", 0)
	})
	t.Run("no attacks nearby", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		playTicks(r, clickTimes(periodic([]int{2}, 200), 0))
		expectCount(t, r.run("autoclicker"), "autoclicker.consistency", 0)
	})
	t.Run("creative", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		r.state(func(s *pb.StateChange) { s.GameMode = pb.GameMode_GAME_MODE_CREATIVE })
		playTicks(r, plans)
		expectCount(t, r.run("autoclicker"), "autoclicker.consistency", 0)
	})
}

func TestAutoclickerClicksBeforeAnAttackCount(t *testing.T) {
	// Swings at air in the 1.5 s before a hit are clicks too.
	r := newRig(t, rigProtocol)
	plans := clickTimes(periodic([]int{2}, 200), 0)
	for i := 28; i < len(plans); i += 29 {
		plans[i].attack = true
	}
	playTicks(r, plans)
	expectCount(t, r.run("autoclicker"), "autoclicker.consistency", 5)
}
