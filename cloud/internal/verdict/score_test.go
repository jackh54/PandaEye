package verdict

import (
	"math"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func spec(t *testing.T, id string) checks.Spec {
	t.Helper()
	s, ok := checks.Lookup(id)
	if !ok {
		t.Fatalf("unknown check %s", id)
	}
	return s
}

func TestThresholdsByLeniency(t *testing.T) {
	for l, m := range map[int]float64{1: 0.6, 2: 0.8, 3: 1.0, 4: 1.3, 5: 1.7, 0: 1.3, -1: 1.3, 6: 1.3} {
		th := ThresholdsFor(l)
		if !near(th.Alert, 15*m) || !near(th.Mitigate, 30*m) || !near(th.Kick, 70*m) || !near(th.Ban, 120*m) {
			t.Errorf("leniency %d: %+v", l, th)
		}
	}
	if NormalizeLeniency(0) != 4 || NormalizeLeniency(2) != 2 {
		t.Fatal("NormalizeLeniency")
	}
}

func TestDecayByHalfLife(t *testing.T) {
	var s scoreState
	t0 := int64(1_700_000_000_000_000)
	s.advance(t0)
	s.add(spec(t, "killaura.hitbox"), 10, 0.85, "", t0) // half-life 300 s
	s.add(spec(t, "timer.balance"), 8, 0.85, "", t0)    // half-life 300 s
	s.add(spec(t, "movement.speed"), 8, 0.75, "", t0)   // half-life 120 s
	s.advance(t0 + 300*usPerSecond)
	if got := s.checks["killaura.hitbox"].acc; !near(got, 5) {
		t.Fatalf("one half-life: acc = %v, want 5", got)
	}
	if got := s.checks["movement.speed"].acc; !near(got, 8*math.Exp2(-300.0/120)) {
		t.Fatalf("per-check half-life: acc = %v", got)
	}
	// dt = max(0, t - last_t): an earlier time neither decays nor moves last_t back.
	s.advance(t0)
	if got := s.checks["killaura.hitbox"].acc; !near(got, 5) || s.lastT != t0+300*usPerSecond {
		t.Fatalf("out-of-order time decayed: acc %v lastT %d", got, s.lastT)
	}
	// acc = acc * 2^(-dt/h) + points
	s.add(spec(t, "killaura.hitbox"), 5, 0.85, "", t0)
	if got := s.checks["killaura.hitbox"].acc; !near(got, 10) {
		t.Fatalf("add after decay: %v", got)
	}
}

func TestCapsFamilyCapsAndConfidence(t *testing.T) {
	var s scoreState
	t0 := int64(1_700_000_000_000_000)
	s.advance(t0)
	s.add(spec(t, "reach.distance"), 200, 0.9, "", t0)   // cap 80, family reach 80
	s.add(spec(t, "aim.gcd"), 100, 0.75, "", t0)         // cap 30
	s.add(spec(t, "aim.snap"), 100, 0.8, "", t0)         // cap 50; family aim caps 30+50 at 45
	s.add(spec(t, "killaura.hitbox"), 5, 0.85, "c1", t0) // family killaura 5
	s.add(spec(t, "latency.blink"), 0.6, 0.99, "", t0)   // acc < 1: in S, not in C
	sc := s.snapshot()
	want := 80 + 45 + 5 + 0.6
	if !near(sc.S, want) {
		t.Fatalf("S = %v, want %v", sc.S, want)
	}
	wantC := 1 - (1-0.9)*(1-0.75)*(1-0.8)*(1-0.85)
	if !near(sc.C, wantC) {
		t.Fatalf("C = %v, want %v", sc.C, wantC)
	}
	if sc.FamiliesOver5 != 3 {
		t.Fatalf("families >= 5: %d", sc.FamiliesOver5)
	}
	fam := map[string]FamilyContribution{}
	for _, f := range sc.Families {
		fam[f.Family] = f
	}
	if fam["aim"].Contribution != 45 || fam["aim"].Cap != 45 || fam["reach"].Contribution != 80 {
		t.Fatalf("families: %+v", sc.Families)
	}
	if sc.Checks[0].Check != "reach.distance" || sc.Checks[0].Contribution != 80 || sc.Checks[0].Acc != 200 {
		t.Fatalf("strongest check: %+v", sc.Checks[0])
	}
	if sc.Dominant != pb.Category_CATEGORY_HEURISTICS {
		t.Fatalf("dominant %v", sc.Dominant)
	}
	for _, c := range sc.Contributing() {
		if c.Check == "latency.blink" {
			t.Fatal("acc < 1 must not contribute")
		}
	}
	if !hasClip(sc) || len(clipIDs(sc.Contributing())) != 1 {
		t.Fatal("clip of a contributing check")
	}
}

func TestEpisodeResetsWhenEmpty(t *testing.T) {
	var s scoreState
	t0 := int64(1_700_000_000_000_000)
	s.advance(t0)
	s.add(spec(t, "aim.snap"), 5, 0.95, "clip-1", t0)
	s.add(spec(t, "aim.snap"), 5, 0.8, "", t0)
	if c := s.checks["aim.snap"]; c.count != 2 || c.conf != 0.95 || len(c.clips) != 1 {
		t.Fatalf("episode %+v", c)
	}
	t1 := t0 + int64(3*time.Hour/time.Microsecond)
	s.advance(t1) // 10 * 2^-36: empty
	s.add(spec(t, "aim.snap"), 5, 0.8, "", t1)
	if c := s.checks["aim.snap"]; c.count != 1 || c.conf != 0.8 || len(c.clips) != 0 || c.firstT != t1 {
		t.Fatalf("new episode %+v", c)
	}
}

func TestUnknownChecks(t *testing.T) {
	sp := specFor(&pb.Signal{CheckId: "ml.gbdt", Category: pb.Category_CATEGORY_HEURISTICS})
	if sp.Cap != 50 || sp.HalfLife != 300*time.Second || familyCap("ml") != 50 || !CloudCheck("ml.gbdt") {
		t.Fatalf("ml spec %+v", sp)
	}
	sp = specFor(&pb.Signal{CheckId: "future.check", Local: true})
	if sp.Where != checks.Local || familyCap("future") != unknownFamilyCap || CloudCheck("timer.balance") || !CloudCheck("aim.snap") {
		t.Fatalf("unknown spec %+v", sp)
	}
}

func TestMitigationsAndStrength(t *testing.T) {
	th := ThresholdsFor(3) // M 30, K 70
	if mitigationStrength(31, th) != 0.3 || mitigationStrength(50, th) != 0.5 || mitigationStrength(200, th) != 1 {
		t.Fatal("strength clamp")
	}
	for cat, want := range map[pb.Category][]pb.Mitigation{
		pb.Category_CATEGORY_HEURISTICS:        {pb.Mitigation_MITIGATION_HIT_CANCEL, pb.Mitigation_MITIGATION_DAMAGE_REDUCE, pb.Mitigation_MITIGATION_VELOCITY_BOOST},
		pb.Category_CATEGORY_AUTO_CLICKER:      {pb.Mitigation_MITIGATION_HIT_CANCEL, pb.Mitigation_MITIGATION_DAMAGE_REDUCE, pb.Mitigation_MITIGATION_VELOCITY_BOOST},
		pb.Category_CATEGORY_MOVEMENT:          {pb.Mitigation_MITIGATION_SETBACK},
		pb.Category_CATEGORY_TICK_SPEED:        {pb.Mitigation_MITIGATION_SETBACK},
		pb.Category_CATEGORY_WORLD_INTERACTION: {pb.Mitigation_MITIGATION_PLACE_CANCEL},
		pb.Category_CATEGORY_INVENTORY:         {pb.Mitigation_MITIGATION_HIT_CANCEL},
	} {
		got := mitigationsFor(cat)
		if len(got) != len(want) {
			t.Fatalf("%v: %v", cat, got)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%v: %v", cat, got)
			}
		}
	}
}

func TestIDSetEvictsOldest(t *testing.T) {
	s := newIDSet(3)
	for _, id := range []string{"a", "b", "c"} {
		if !s.add(id) {
			t.Fatal(id)
		}
	}
	if s.add("a") {
		t.Fatal("duplicate accepted")
	}
	s.add("d") // evicts a
	if !s.add("a") || s.add("d") {
		t.Fatal("FIFO eviction")
	}
}
