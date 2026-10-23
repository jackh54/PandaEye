package checks

import (
	"math"
	"slices"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
)

// The eye heights of a block action's candidates: a standing or sneaking action keeps the standing and
// sneaking eyes; a swimming/gliding one (real crawl or swim, or a stale flag) adds 0.4 at the same
// feet, on the path from the previous position and at the next move.
func TestBlockCandidateHeights(t *testing.T) {
	v := NewView()
	v.Protocol = 769
	prev, feet, next := geom.V(0.5, 64, 0.7), geom.V(0.5, 64, 0.5), geom.V(0.5, 64, 0.3)
	heights := func(pose geom.Pose) (event, path, after []float64) {
		a := blockAction{eye: geom.Eye(feet, pose, v.Protocol, 1), pose: pose, prevFeet: prev, hasPrev: true}
		var c blockCandidates
		c.build(v, &a, &Move{HasPos: true, Pos: next})
		add := func(hs []float64, e geom.Vec3) []float64 {
			if h := math.Round((e.Y-64)*100) / 100; !slices.Contains(hs, h) {
				hs = append(hs, h)
				slices.Sort(hs)
			}
			return hs
		}
		for _, e := range c.eyes() {
			switch e.Z {
			case feet.Z:
				event = add(event, e)
			case next.Z:
				after = add(after, e)
			default:
				path = add(path, e)
			}
		}
		return event, path, after
	}
	upright, reduced := []float64{1.27, 1.62}, []float64{0.4, 1.27, 1.62}
	for _, c := range []struct {
		pose geom.Pose
		want []float64
	}{{geom.PoseStanding, upright}, {geom.PoseSneaking, upright}, {geom.PoseSwimming, reduced}} {
		event, path, after := heights(c.pose)
		if !slices.Equal(event, c.want) || !slices.Equal(path, c.want) || !slices.Equal(after, c.want) {
			t.Errorf("pose %d: event %v, path %v, next move %v; want %v each", c.pose, event, path, after, c.want)
		}
	}
}
