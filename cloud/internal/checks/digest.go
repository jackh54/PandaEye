package checks

import (
	"sort"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
)

// Check-health digest (CHECKS.md section 5): the world-interaction checks report every judged placement
// or dig as an Observation and every other emission is counted, to an optional DigestSink. The worker
// aggregates them into hourly counts per server, client and situation, without player data. An
// observation carries the check's outcome, its decisive quantity and the candidates tried; the sink adds
// the view's server, client protocol, brand family, tick-client flag and ping.

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
