package checks

import (
	"math"
	"slices"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
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

// scaffold.cursor adds the 0.4 eyes (at the event feet, on the path and at the next move) only for a
// bottom-face click whose cursor point lies below every standing and sneaking candidate by more than the
// eye uncertainty, only when 0.4 is not already a candidate and only for clients with the crawl pose.
func TestCursorEyesCrawlCase(t *testing.T) {
	prev, feet, next := geom.V(0.5, 64, 0.7), geom.V(0.5, 64, 0.5), geom.V(0.5, 64, 0.3)
	cand := func(protocol int32, pose geom.Pose) *blockCandidates {
		v := NewView()
		v.Protocol = protocol
		a := blockAction{eye: geom.Eye(feet, pose, protocol, 1), pose: pose, prevFeet: prev, hasPrev: true}
		var c blockCandidates
		c.build(v, &a, &Move{HasPos: true, Pos: next})
		return &c
	}
	const u = 0.0002 // geom.EyeUncertainty of 1.21
	low := 64 + 1.27 - 2*u
	for _, c := range []struct {
		name     string
		protocol int32
		pose     geom.Pose
		face     pb.Face
		y        float64 // cursor point height
		crawl    bool
	}{
		{"bottom face below every eye", 769, geom.PoseStanding, pb.Face_FACE_DOWN, 65, true},
		{"sneaking report", 769, geom.PoseSneaking, pb.Face_FACE_DOWN, 65, true},
		{"just below the sneaking eyes", 769, geom.PoseStanding, pb.Face_FACE_DOWN, low, true},
		{"within the uncertainty of the sneaking eyes", 769, geom.PoseStanding, pb.Face_FACE_DOWN, 64 + 1.27 - u/2, false},
		{"above the sneaking eyes", 769, geom.PoseStanding, pb.Face_FACE_DOWN, 65.5, false},
		{"within the uncertainty of the crawling eyes", 769, geom.PoseStanding, pb.Face_FACE_DOWN, 64.4 - u/2, true},
		{"below the crawling eyes too", 769, geom.PoseStanding, pb.Face_FACE_DOWN, 64.4 - 2*u, false},
		{"top face", 769, geom.PoseStanding, pb.Face_FACE_UP, 65, false},
		{"side face", 769, geom.PoseStanding, pb.Face_FACE_NORTH, 65, false},
		{"0.4 is a candidate already", 769, geom.PoseSwimming, pb.Face_FACE_DOWN, 65, false},
		{"client without the crawl pose", 404, geom.PoseStanding, pb.Face_FACE_DOWN, 65, false},
	} {
		b := cand(c.protocol, c.pose)
		eyes := b.cursorEyes(c.face, geom.V(0.5, c.y, -0.2), u)
		var crawl []geom.Vec3
		for _, e := range eyes[b.nEye:] {
			if math.Abs(e.Y-64.4) > 1e-9 {
				t.Errorf("%s: added eye %v is not at 0.4", c.name, e)
			}
			crawl = append(crawl, e)
		}
		if got := len(crawl) > 0; got != c.crawl {
			t.Errorf("%s: crawl eyes added = %v, want %v", c.name, got, c.crawl)
		}
		if c.crawl && len(crawl) != 2+eyePathSamples {
			t.Errorf("%s: %d crawl eyes, want the event feet, %d path points and the next move", c.name, len(crawl), eyePathSamples)
		}
		if !slices.Equal(eyes[:b.nEye], b.eyes()) {
			t.Errorf("%s: the candidates changed", c.name)
		}
	}
}
