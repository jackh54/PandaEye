package checks

import (
	"sort"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Check-health digest (CHECKS.md section 5): the world-interaction checks report every judged placement
// or dig as an Observation and every other emission is counted, to an optional DigestSink. The worker
// aggregates them into hourly counts per server, client and situation, without player data. An
// observation carries the check's outcome, its decisive quantity, the candidates tried and the action's
// context (DigestContext); the sink adds the view's server, client protocol, brand family, tick-client
// flag and ping.

// Observation is one judged placement or dig of a world-interaction check.
type Observation struct {
	Check   string
	Pose    geom.Pose // the eye pose the plugin reported for the action
	Failed  bool      // the per-event result, before the "k of the last n" rule
	Emitted bool      // a signal was emitted for this event
	// Value is the decisive quantity: scaffold.cursor the best angle in degrees; scaffold.ray and dig.ray
	// the angle in degrees from the nearest candidate ray to the expanded cube (0 when a ray hits it, also
	// beyond the reach limit); scaffold.face the visibility margin in blocks (visible when > 0);
	// scaffold.reach the distance beyond the limit in blocks (a violation when > 0).
	Value   float64
	EyePath bool // the eyes on the path from the previous position were candidates
	// Distinct eye and rotation candidates of the action (scaffold.face and scaffold.reach use the eyes
	// only).
	Eyes, Rotations int
	// The action's context, the same for every check of the action (DigestContext buckets it): Dist is the
	// distance in blocks from the event eye to the block's centre, Turn the angle in degrees between the R0
	// and R1 look vectors (0 without an R1), Step the horizontal length in blocks of the eye path's segment
	// (with EyePath), GapUs the time since the detector's previous judged action (the player's previous
	// placement for scaffold.*, dig for dig.ray), -1 for the session's first.
	Dist, Turn, Step float64
	GapUs            int64
	Face             pb.Face            // the clicked (or dug) face
	Action           pb.BlockDig_Action // dig.ray: START or FINISH
	RayHit           bool               // scaffold.cursor: scaffold.ray hit the cube for the same placement
}

// DigestSink receives the check-health digest of one player's checks. It is called from the goroutine
// handling the player's events: it must be cheap, must not block and must not retain v.
type DigestSink interface {
	// Observe records one judged placement or dig.
	Observe(v *View, o Observation)
	// Emitted records an emission of a check that reports no observations.
	Emitted(v *View, check string)
}

// DigestBuckets is the size of the largest DigestEdges histogram.
const DigestBuckets = 18

var (
	digestAngleEdges  = []float64{0, 0.25, 0.5, 1, 1.5, 2, 2.5, 3, 4, 5, 7.5, 10, 15, 20, 30, 45, 90}
	digestMarginEdges = []float64{-3, -2, -1, -0.5, -0.25, -0.1, -0.05, -0.02, 0, 0.02, 0.05, 0.1, 0.25, 0.5, 1, 2, 3}
	digestExcessEdges = []float64{-4, -3, -2, -1.5, -1, -0.5, -0.25, -0.1, 0, 0.1, 0.25, 0.5, 1, 1.5, 2, 3}
)

// DigestEdges returns the histogram edges of a check's Observation.Value, nil for a check without
// observations. Edges are upper-inclusive: bucket i counts values in (edges[i-1], edges[i]], the last
// bucket the values above the last edge. The slice is shared and must not be modified.
func DigestEdges(check string) []float64 {
	switch check {
	case idScaffoldCursor, idScaffoldRay, idDigRay:
		return digestAngleEdges
	case idScaffoldFace:
		return digestMarginEdges
	case idScaffoldReach:
		return digestExcessEdges
	}
	return nil
}

// DigestBucket returns the bucket of value in edges (DigestEdges).
func DigestBucket(edges []float64, value float64) int { return sort.SearchFloat64s(edges, value) }

// DigestContext is a breakdown of the world-interaction cells by the context of the judged actions: each
// cell counts its evaluations and failures per bucket, so failure rates compare across situations.
type DigestContext uint8

const (
	DigestDist   DigestContext = iota // Observation.Dist: < 1.5, 1.5-3, 3-4.5, >= 4.5 blocks
	DigestTurn                        // Observation.Turn: 0, (0, 5], (5, 30], > 30 degrees
	DigestGap                         // Observation.GapUs: < 60 ms, 60-250 ms, 250 ms-1 s, >= 1 s or the first
	DigestSpeed                       // Observation.Step: no eye path, < 0.05, 0.05-0.2, 0.2-0.4, >= 0.4 blocks
	DigestFace                        // Observation.Face of a placement: up, down, side, other
	DigestAction                      // Observation.Action of a dig: start, finish
	NumDigestContexts
)

// DigestContextBuckets is the size of the largest DigestContext.
const DigestContextBuckets = 5

var (
	digestContextNames   = [NumDigestContexts]string{"dist", "turn", "gap", "speed", "face", "action"}
	digestContextBuckets = [NumDigestContexts][]string{
		{"<1.5", "1.5-3", "3-4.5", ">=4.5"},
		{"0", "0-5", "5-30", ">30"},
		{"<60", "60-250", "250-1000", ">=1000"},
		{"none", "<0.05", "0.05-0.2", "0.2-0.4", ">=0.4"},
		{"up", "down", "side", "other"},
		{"start", "finish"},
	}
	// Lower bounds of the buckets after the first (dist and speed in blocks, gap in microseconds); the turn
	// buckets are upper-inclusive, like the histograms.
	digestDistFrom  = []float64{1.5, 3, 4.5}
	digestTurnEdges = []float64{0, 5, 30}
	digestGapFrom   = []float64{60_000, 250_000, 1_000_000}
	digestStepFrom  = []float64{0.05, 0.2, 0.4}

	digestPlaceContexts = []DigestContext{DigestDist, DigestTurn, DigestGap, DigestSpeed, DigestFace}
	digestDigContexts   = []DigestContext{DigestDist, DigestTurn, DigestGap, DigestSpeed, DigestAction}
)

// DigestContexts returns the breakdowns of a check's cells, nil for a check without observations: the
// placements' end with the clicked face, the digs' with the action. The slice is shared and must not be
// modified.
func DigestContexts(check string) []DigestContext {
	switch check {
	case idScaffoldFace, idScaffoldRay, idScaffoldCursor, idScaffoldReach:
		return digestPlaceContexts
	case idDigRay:
		return digestDigContexts
	}
	return nil
}

// DigestCursorOnly reports whether a check's cells count the failures for which scaffold.ray hit the cube
// of the same placement (Observation.RayHit).
func DigestCursorOnly(check string) bool { return check == idScaffoldCursor }

func (d DigestContext) String() string { return digestContextNames[d] }

// Buckets returns the names of d's buckets. The slice is shared and must not be modified.
func (d DigestContext) Buckets() []string { return digestContextBuckets[d] }

// Bucket returns the bucket of o's action in d.
func (d DigestContext) Bucket(o *Observation) int {
	switch d {
	case DigestDist:
		return digestFrom(digestDistFrom, o.Dist)
	case DigestTurn:
		return DigestBucket(digestTurnEdges, o.Turn)
	case DigestGap:
		if o.GapUs < 0 {
			return len(digestGapFrom) // the first: no previous action
		}
		return digestFrom(digestGapFrom, float64(o.GapUs))
	case DigestSpeed:
		if !o.EyePath {
			return 0
		}
		return 1 + digestFrom(digestStepFrom, o.Step)
	case DigestFace:
		switch o.Face {
		case pb.Face_FACE_UP:
			return 0
		case pb.Face_FACE_DOWN:
			return 1
		case pb.Face_FACE_NORTH, pb.Face_FACE_SOUTH, pb.Face_FACE_WEST, pb.Face_FACE_EAST:
			return 2
		}
		return 3
	case DigestAction:
		if o.Action == pb.BlockDig_ACTION_FINISH {
			return 1
		}
	}
	return 0
}

// digestFrom returns the number of lower bounds at or below value: its bucket when bucket i+1 starts at
// from[i].
func digestFrom(from []float64, value float64) int {
	n := 0
	for n < len(from) && value >= from[n] {
		n++
	}
	return n
}

// BrandFamily is the client brand (PlayerJoin.brand) reduced to a few families for the digest.
type BrandFamily uint8

const (
	BrandUnknown BrandFamily = iota // no brand received (yet)
	BrandVanilla
	BrandFabric
	BrandForge // Forge and NeoForge
	BrandLunar
	BrandBadlion
	BrandFeather
	BrandLabyMod
	BrandOther
)

var brandNames = [...]string{"unknown", "vanilla", "fabric", "forge", "lunar", "badlion", "feather", "labymod", "other"}

func (b BrandFamily) String() string {
	if int(b) < len(brandNames) {
		return brandNames[b]
	}
	return brandNames[BrandOther]
}

// brandRules are tried in order on the lower-cased brand; the first one whose text it contains decides.
// Clients come before the loaders they are built on ("Feather Fabric" is feather), Forge before Fabric
// (Fabric mods on Forge), and "forge" also matches "neoforge" and "fml,forge".
var brandRules = []struct {
	sub    string
	family BrandFamily
}{
	{"lunar", BrandLunar}, {"badlion", BrandBadlion}, {"feather", BrandFeather}, {"labymod", BrandLabyMod},
	{"forge", BrandForge}, {"fabric", BrandFabric}, {"vanilla", BrandVanilla},
}

// BrandFamilyOf normalises a client brand: unknown when it is empty (or blank), other when no rule
// matches.
func BrandFamilyOf(brand string) BrandFamily {
	b := strings.ToLower(strings.TrimSpace(brand))
	if b == "" {
		return BrandUnknown
	}
	for _, r := range brandRules {
		if strings.Contains(b, r.sub) {
			return r.family
		}
	}
	return BrandOther
}
