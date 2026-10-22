// Package checks holds the cloud checks. catalog.go is the scoring table of every check (CHECKS.md
// section 4, including local checks for scoring); catalog_test.go keeps it in sync with the doc.
package checks

import (
	"strings"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Where a check runs.
type Where string

const (
	Local Where = "local" // in the plugin; arrives as a forwarded LocalFlag
	Cloud Where = "cloud" // in the workers
)

// ProbeCheckID is the check id of latency probe signals (0 points, never scored).
const ProbeCheckID = "probe"

// Spec is the scoring entry of one check (CHECKS.md section 4). Points are computed by the check
// itself (Signal.points carries raw points); confidence is applied by the verdict service.
type Spec struct {
	ID            string
	Family        string // id prefix before the first dot
	Category      pb.Category
	Where         Where
	Confidence    float64 // default (lowest) confidence
	MaxConfidence float64 // highest confidence for checks with a range ("0.7..0.95"), else = Confidence
	HalfLife      time.Duration
	Cap           float64 // max contribution of the check's accumulator
	Certain       bool    // may justify a BAN alone (ARCHITECTURE 5.6)
	// Experimental checks are not yet validated on live traffic: they count towards the score (ALERT,
	// MITIGATE) but KICK and BAN also need a stable family (CHECKS.md 4.1).
	Experimental   bool
	EnabledDefault bool // autoclicker.cps is off by default
	Variants       []string
}

func spec(id string, cat pb.Category, where Where, conf, maxConf float64, halfLifeS int, cap float64, certain bool) Spec {
	if maxConf == 0 {
		maxConf = conf
	}
	return Spec{ID: id, Family: Family(id), Category: cat, Where: where, Confidence: conf, MaxConfidence: maxConf,
		HalfLife: time.Duration(halfLifeS) * time.Second, Cap: cap, Certain: certain, EnabledDefault: true}
}

const (
	catAutoClicker = pb.Category_CATEGORY_AUTO_CLICKER
	catHeuristics  = pb.Category_CATEGORY_HEURISTICS
	catProtocol    = pb.Category_CATEGORY_INVALID_PROTOCOL
	catMovement    = pb.Category_CATEGORY_MOVEMENT
	catTickSpeed   = pb.Category_CATEGORY_TICK_SPEED
	catInventory   = pb.Category_CATEGORY_INVENTORY
	catWorld       = pb.Category_CATEGORY_WORLD_INTERACTION
	catLatency     = pb.Category_CATEGORY_LATENCY_ABUSE
)

// catalog is the table of CHECKS.md section 4, in its order. catalog_test.go asserts they match.
var catalog = func() []Spec {
	s := []Spec{
		spec("timer.balance", catTickSpeed, Local, 0.85, 0, 300, 60, false),
		spec("movement.speed", catMovement, Local, 0.75, 0, 120, 50, false),
		spec("movement.fly", catMovement, Local, 0.8, 0, 120, 60, false),
		spec("movement.step", catMovement, Local, 0.8, 0, 120, 50, false),
		spec("movement.groundspoof", catMovement, Local, 0.8, 0, 300, 40, false),
		spec("movement.noslow", catMovement, Local, 0.75, 0, 120, 40, false),
		spec("dig.speed", catWorld, Local, 0.8, 0, 300, 50, false),
		spec("protocol.pitch", catProtocol, Local, 1.0, 0, 3600, 200, true),
		spec("protocol.position", catProtocol, Local, 1.0, 0, 3600, 200, true),
		spec("protocol.slot", catProtocol, Local, 0.95, 0, 600, 20, false),
		spec("protocol.selfhit", catProtocol, Local, 1.0, 0, 3600, 200, true),
		spec("protocol.cursor", catProtocol, Local, 0.6, 0, 600, 15, false),
		spec("protocol.flood", catProtocol, Local, 0.7, 0, 600, 20, false),
		spec("protocol.abilities", catProtocol, Local, 0.9, 0, 600, 30, false),
		spec("inventory.closed", catInventory, Local, 0.8, 0, 300, 20, false),
		spec("reach.distance", catHeuristics, Cloud, 0.7, 0.95, 300, 80, false),
		spec("killaura.hitbox", catHeuristics, Cloud, 0.85, 0, 300, 80, false),
		spec("killaura.multi", catHeuristics, Cloud, 0.95, 0, 900, 120, true),
		spec("killaura.noswing", catHeuristics, Cloud, 0.9, 0, 600, 80, false),
		spec("aim.gcd", catHeuristics, Cloud, 0.75, 0, 300, 30, false),
		spec("aim.snap", catHeuristics, Cloud, 0.8, 0, 300, 50, false),
		spec("aim.precision", catHeuristics, Cloud, 0.7, 0, 300, 30, false),
		spec("velocity.vertical", catHeuristics, Cloud, 0.85, 0, 300, 60, false),
		spec("velocity.horizontal", catHeuristics, Cloud, 0.8, 0, 300, 50, false),
		spec("autoclicker.consistency", catAutoClicker, Cloud, 0.7, 0, 300, 30, false),
		spec("autoclicker.pattern", catAutoClicker, Cloud, 0.7, 0, 300, 30, false),
		spec("autoclicker.cps", catAutoClicker, Cloud, 0.6, 0, 300, 30, false),
		spec("scaffold.face", catWorld, Cloud, 0.95, 0, 600, 80, false),
		spec("scaffold.ray", catWorld, Cloud, 0.85, 0, 300, 80, false),
		spec("scaffold.cursor", catWorld, Cloud, 0.8, 0, 300, 60, false),
		spec("scaffold.reach", catWorld, Cloud, 0.8, 0, 300, 50, false),
		spec("dig.ray", catWorld, Cloud, 0.85, 0, 300, 60, false),
		spec("inventory.move", catInventory, Cloud, 0.75, 0, 300, 30, false),
		spec("inventory.rotate", catInventory, Cloud, 0.7, 0, 300, 30, false),
		spec("latency.pingspoof", catLatency, Cloud, 0.8, 0, 600, 18, false),
		spec("latency.blink", catLatency, Cloud, 0.6, 0, 300, 18, false),
	}
	for i := range s {
		switch s[i].ID {
		case "autoclicker.cps":
			s[i].EnabledDefault = false // drag/butterfly clicking is a server policy question
		case "protocol.slot":
			s[i].Variants = []string{"range", "redundant"} // redundant scores 0 points
		}
		s[i].Experimental = experimental[s[i].ID]
		if s[i].Experimental && s[i].Certain {
			panic("checks: a certain check cannot be experimental: " + s[i].ID)
		}
	}
	return s
}()

// experimental lists the checks of CHECKS.md 4.1. The first live shadow run showed block-interaction
// geometry, velocity and ping checks firing on legit players (eye interpolation, plugins that rewrite
// velocity packets or hide pongs); they stay experimental until shadow data clears them.
var experimental = map[string]bool{
	"dig.speed": true, "dig.ray": true,
	"scaffold.face": true, "scaffold.ray": true, "scaffold.cursor": true, "scaffold.reach": true,
	"velocity.vertical": true, "velocity.horizontal": true,
	"latency.pingspoof": true, "latency.blink": true,
}

// familyCaps caps the sum of a family's check contributions (CHECKS.md section 4).
var familyCaps = map[string]float64{
	"timer": 60, "movement": 80, "protocol": 200, "inventory": 40, "reach": 80, "killaura": 120, "aim": 45,
	"velocity": 60, "autoclicker": 40, "scaffold": 100, "dig": 70, "latency": 25, "ml": 50,
}

var byID = func() map[string]Spec {
	m := make(map[string]Spec, len(catalog))
	for _, s := range catalog {
		m[s.ID] = s
	}
	return m
}()

// Catalog returns every check in CHECKS.md order. ml.<model> checks are not listed: their scoring
// depends on the model (CHECKS.md 2.16); their family cap is FamilyCap("ml").
func Catalog() []Spec { return append([]Spec(nil), catalog...) }

// Lookup returns the spec of a check id.
func Lookup(id string) (Spec, bool) {
	s, ok := byID[id]
	return s, ok
}

// Family returns the family of a check id: the prefix before the first dot.
func Family(id string) string {
	f, _, _ := strings.Cut(id, ".")
	return f
}

// FamilyCap returns the cap of a family.
func FamilyCap(family string) (float64, bool) {
	c, ok := familyCaps[family]
	return c, ok
}

// FamilyCaps returns a copy of every family cap.
func FamilyCaps() map[string]float64 {
	out := make(map[string]float64, len(familyCaps))
	for k, v := range familyCaps {
		out[k] = v
	}
	return out
}
