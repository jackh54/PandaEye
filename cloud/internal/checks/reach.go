package checks

import (
	"fmt"
	"math"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// reach.distance (CHECKS.md 2.2): dist = min distance from any eye candidate to any target box
// candidate of the attack timing window, limit = entity reach + eye uncertainty, violation when the
// excess is above 0.05 on 2 of the player's last 5 judged attacks on players.

// ReachCheck is the check id of reach.distance.
const ReachCheck = "reach.distance"

const (
	reachWindow     = 5
	reachViolations = 2
	reachMinExcess  = 0.05
)

func init() {
	Register(Factory{Name: "reach", Checks: []string{ReachCheck}, New: func() Detector {
		return &reachDetector{recent: NewRing[bool](reachWindow)}
	}})
}

type reachDetector struct {
	recent Ring[bool] // excess above the threshold, per judged attack on a player
}

func (d *reachDetector) Handle(c *Ctx, ev *pb.Event) {
	if ev.GetAttack() == nil {
		return
	}
	a, _ := NewAttackObs(c.View, ev)
	if a.SkipReason(true) != "" {
		return
	}
	dist, limit, ok := a.ReachDistance()
	if !ok {
		return
	}
	excess := dist - limit
	over := excess > c.Param(ReachCheck, "min_excess", reachMinExcess)
	d.recent.Push(over)
	if !over {
		return
	}
	n := cwCountTrue(&d.recent)
	if float64(n) < c.Param(ReachCheck, "violations", reachViolations) {
		return
	}
	c.Emit(Emission{
		Check:      ReachCheck,
		Points:     3 + 10*math.Min(excess, 1.5)/1.5,
		Confidence: 0.7 + 0.25*math.Min(excess/0.5, 1),
		Info:       fmt.Sprintf("reach %.3f > %.3f (%d of the last %d attacks)", dist, limit, n, d.recent.Len()),
		Data: map[string]float64{"dist": dist, "limit": limit, "excess": excess, "ping_ms": float64(a.PingMs),
			"rtt_high_ms": float64(a.RTTHigh()), "server_tick_ms": a.ServerTickMs},
	})
}
