package verdict

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Base thresholds at leniency 3 (ARCHITECTURE 5.6).
const (
	BaseAlert    = 15.0
	BaseMitigate = 30.0
	BaseKick     = 70.0
	BaseBan      = 120.0

	// DefaultLeniency applies when a tenant has no (valid) leniency.
	DefaultLeniency = 4
)

// leniencyMultipliers maps leniency 1..5 to the threshold multiplier.
var leniencyMultipliers = [...]float64{1: 0.6, 2: 0.8, 3: 1.0, 4: 1.3, 5: 1.7}

// Accumulators below this are considered empty (state eviction, new episodes).
const emptyAcc = 0.5

// Defaults for checks the catalog does not know (ml.<model> and checks newer than this binary).
const (
	unknownHalfLife  = 300 * time.Second
	unknownCheckCap  = 30.0
	unknownFamilyCap = 50.0
	mlCheckCap       = 50.0
)

// Thresholds are the action thresholds of one leniency.
type Thresholds struct {
	Alert, Mitigate, Kick, Ban float64
}

// NormalizeLeniency returns l when it is 1..5, else DefaultLeniency.
func NormalizeLeniency(l int) int {
	if l < 1 || l > 5 {
		return DefaultLeniency
	}
	return l
}

// LeniencyMultiplier returns the threshold multiplier of a leniency (invalid values use the default).
func LeniencyMultiplier(l int) float64 { return leniencyMultipliers[NormalizeLeniency(l)] }

// ThresholdsFor returns the thresholds of a leniency: the base thresholds times its multiplier.
func ThresholdsFor(leniency int) Thresholds {
	m := LeniencyMultiplier(leniency)
	return Thresholds{Alert: BaseAlert * m, Mitigate: BaseMitigate * m, Kick: BaseKick * m, Ban: BaseBan * m}
}

// specFor returns the scoring spec of a signal's check: the catalog entry, or defaults for unknown
// checks (category from the signal; ml.<model> capped like its family).
func specFor(sig *pb.Signal) checks.Spec {
	if s, ok := checks.Lookup(sig.CheckId); ok {
		return s
	}
	s := checks.Spec{ID: sig.CheckId, Family: checks.Family(sig.CheckId), Category: sig.Category, Where: checks.Cloud,
		Confidence: 0.5, MaxConfidence: 1, HalfLife: unknownHalfLife, Cap: unknownCheckCap, EnabledDefault: true}
	if sig.Local {
		s.Where = checks.Local
	}
	if s.Family == "ml" {
		s.Cap = mlCheckCap
	}
	return s
}

func familyCap(family string) float64 {
	if c, ok := checks.FamilyCap(family); ok {
		return c
	}
	return unknownFamilyCap
}

// maxClipsPerCheck bounds the committed clip ids remembered per check.
const maxClipsPerCheck = 3

// checkState is the accumulator of one check for one player.
type checkState struct {
	spec   checks.Spec
	acc    float64
	conf   float64  // highest confidence of the current episode
	count  int      // signals of the current episode
	clips  []string // committed clips of the current episode, newest last
	firstT int64    // normalised time of the episode's first signal (legit-cover window)
}

// scoreState holds a player's accumulators. Times are normalised Unix microseconds.
type scoreState struct {
	lastT  int64
	checks map[string]*checkState
}

// advance decays every accumulator to t: dt = max(0, t - lastT), acc *= 2^(-dt/halflife).
func (s *scoreState) advance(t int64) {
	if t <= s.lastT {
		return
	}
	if s.lastT != 0 {
		dt := float64(t - s.lastT)
		for _, c := range s.checks {
			c.acc *= math.Exp2(-dt / float64(c.spec.HalfLife.Microseconds()))
		}
	}
	s.lastT = t
}

// add applies one signal at time t (call advance(t) first): acc += points.
func (s *scoreState) add(spec checks.Spec, points, conf float64, clip string, t int64) {
	if s.checks == nil {
		s.checks = map[string]*checkState{}
	}
	if spec.HalfLife <= 0 {
		spec.HalfLife = unknownHalfLife
	}
	c := s.checks[spec.ID]
	if c == nil {
		c = &checkState{spec: spec}
		s.checks[spec.ID] = c
	}
	if c.acc < emptyAcc {
		c.acc, c.conf, c.count, c.clips, c.firstT = 0, 0, 0, nil, t
	}
	c.acc += points
	c.count++
	c.conf = max(c.conf, conf)
	if clip != "" && !slices.Contains(c.clips, clip) {
		c.clips = append(c.clips, clip)
		if len(c.clips) > maxClipsPerCheck {
			c.clips = c.clips[1:]
		}
	}
}

// decayedMax returns the largest accumulator decayed to t without changing the state.
func (s *scoreState) decayedMax(t int64) float64 {
	m := 0.0
	for _, c := range s.checks {
		a := c.acc
		if t > s.lastT && s.lastT != 0 {
			a *= math.Exp2(-float64(t-s.lastT) / float64(c.spec.HalfLife.Microseconds()))
		}
		m = max(m, a)
	}
	return m
}

// CheckContribution is one check's part of the score.
type CheckContribution struct {
	Check        string
	Family       string
	Category     pb.Category
	Acc          float64 // raw (decayed, uncapped) accumulator
	Contribution float64 // min(acc, cap)
	Cap          float64
	Confidence   float64
	Count        int
	Certain      bool
	Cloud        bool // computed by the cloud (not a forwarded local flag)
	Clips        []string
	FirstT       int64
}

// FamilyContribution is one family's capped part of the score.
type FamilyContribution struct {
	Family       string
	Contribution float64 // min(family cap, sum of the family's check contributions)
	Cap          float64
}

// Score is a player's score at the state's last time.
type Score struct {
	S float64 // sum over families of min(familyCap, sum over its checks of min(acc, cap))
	C float64 // 1 - prod(1 - conf) over checks with acc >= 1

	Checks        []CheckContribution  // checks with acc >= emptyAcc, strongest first
	Families      []FamilyContribution // families with a contribution, strongest first
	FamiliesOver5 int                  // families contributing >= 5
	StableOver5   int                  // of those, families whose stable (non-experimental) checks contribute >= 5
	Dominant      pb.Category          // category with the largest contribution
	CertainAcc    float64              // largest accumulator of a certain check
}

// Contributing returns the checks with acc >= 1 (they count for C, are listed in verdicts and
// provide clips), strongest first.
func (sc *Score) Contributing() []CheckContribution {
	var out []CheckContribution
	for _, c := range sc.Checks {
		if c.Acc >= 1 {
			out = append(out, c)
		}
	}
	return out
}

// snapshot computes the score. Sums run in check-id order so the result is deterministic.
func (s *scoreState) snapshot() Score {
	var sc Score
	ids := make([]string, 0, len(s.checks))
	for id := range s.checks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	famSum := map[string]float64{}
	stableSum := map[string]float64{}
	var famOrder []string
	catSum := map[pb.Category]float64{}
	notConf := 1.0
	for _, id := range ids {
		c := s.checks[id]
		contrib := min(c.acc, c.spec.Cap)
		if _, ok := famSum[c.spec.Family]; !ok {
			famOrder = append(famOrder, c.spec.Family)
		}
		famSum[c.spec.Family] += contrib
		if !c.spec.Experimental {
			stableSum[c.spec.Family] += contrib
		}
		catSum[c.spec.Category] += contrib
		if c.acc >= 1 {
			notConf *= 1 - clamp01(c.conf)
		}
		if c.spec.Certain {
			sc.CertainAcc = max(sc.CertainAcc, c.acc)
		}
		if c.acc >= emptyAcc {
			sc.Checks = append(sc.Checks, CheckContribution{Check: id, Family: c.spec.Family, Category: c.spec.Category, Acc: c.acc,
				Contribution: contrib, Cap: c.spec.Cap, Confidence: c.conf, Count: c.count, Certain: c.spec.Certain,
				Cloud: c.spec.Where == checks.Cloud, Clips: c.clips, FirstT: c.firstT})
		}
	}
	for _, f := range famOrder {
		fc := FamilyContribution{Family: f, Cap: familyCap(f), Contribution: min(familyCap(f), famSum[f])}
		sc.S += fc.Contribution
		if fc.Contribution >= 5 {
			sc.FamiliesOver5++
			if min(familyCap(f), stableSum[f]) >= 5 {
				sc.StableOver5++
			}
		}
		if fc.Contribution > 0 {
			sc.Families = append(sc.Families, fc)
		}
	}
	sc.C = 1 - notConf
	slices.SortStableFunc(sc.Checks, func(a, b CheckContribution) int {
		return cmp.Or(cmp.Compare(b.Contribution, a.Contribution), cmp.Compare(a.Check, b.Check))
	})
	slices.SortStableFunc(sc.Families, func(a, b FamilyContribution) int {
		return cmp.Or(cmp.Compare(b.Contribution, a.Contribution), cmp.Compare(a.Family, b.Family))
	})
	best := -1.0
	for cat := pb.Category_CATEGORY_AUTO_CLICKER; cat <= pb.Category_CATEGORY_LATENCY_ABUSE; cat++ {
		if v := catSum[cat]; v > best && v > 0 {
			best, sc.Dominant = v, cat
		}
	}
	if sc.Dominant == pb.Category_CATEGORY_UNSPECIFIED {
		for cat, v := range catSum { // categories outside the known range (newer protocol)
			if v > best {
				best, sc.Dominant = v, cat
			}
		}
	}
	return sc
}

func clamp01(v float64) float64 {
	switch {
	case v < 0 || math.IsNaN(v):
		return 0
	case v > 1:
		return 1
	}
	return v
}

// finite returns v, or 0 for NaN and infinities.
func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
