package checks

import (
	"math"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Forwarded local flags (CHECKS.md section 3): the worker turns every plugin LocalFlag into a signal
// with local = true and the points of section 1 (severity-scaled where the formula says so) and the
// catalog confidence.

// localPoints maps a local check id to its points as a function of the flag's severity (0..1).
var localPoints = map[string]func(severity float64) float64{
	"timer.balance":        func(float64) float64 { return 6 },
	"movement.speed":       func(s float64) float64 { return 2 + 4*s },
	"movement.fly":         func(s float64) float64 { return 4 + 6*s },
	"movement.step":        func(s float64) float64 { return 4 + 4*s },
	"movement.groundspoof": func(s float64) float64 { return 3 + 3*s },
	"movement.noslow":      func(s float64) float64 { return 3 + 3*s },
	"dig.speed":            func(s float64) float64 { return 3 + 4*s },
	"protocol.pitch":       func(float64) float64 { return 25 },
	"protocol.position":    func(float64) float64 { return 25 },
	"protocol.slot":        func(float64) float64 { return 10 }, // variant "range"; "redundant" scores 0
	"protocol.selfhit":     func(float64) float64 { return 25 },
	"protocol.cursor":      func(float64) float64 { return 3 },
	"protocol.flood":       func(float64) float64 { return 3 },
	"protocol.abilities":   func(float64) float64 { return 5 },
	"inventory.closed":     func(float64) float64 { return 3 },
}

// LocalFlagScore returns the scoring of a forwarded LocalFlag: raw points, confidence and the
// check's catalog entry. ok is false for ids that are not local checks of the catalog.
func LocalFlagScore(f *pb.LocalFlag) (points, confidence float64, spec Spec, ok bool) {
	spec, ok = Lookup(f.GetCheckId())
	fn, known := localPoints[f.GetCheckId()]
	if !ok || !known || spec.Where != Local {
		return 0, 0, Spec{}, false
	}
	sev := f.GetSeverity()
	if math.IsNaN(sev) {
		sev = 0
	}
	points = fn(Clamp(sev, 0, 1))
	if f.GetCheckId() == "protocol.slot" && f.GetVariant() == "redundant" {
		points = 0
	}
	return points, spec.Confidence, spec, true
}
