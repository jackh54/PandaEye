package checks

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// rayAngle is exact: never above the angle to any point of the cube, and within the sampling error of a
// dense sampling of its surface.
func TestRayAngleMatchesSampling(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	box := geom.BlockBox(0, 64, 3).Expand(0.05)
	const n = 48
	nearMisses := 0
	for k := 0; k < 300; k++ {
		var c blockCandidates
		c.addEye(geom.V(rng.Float64()*4-2, 64+rng.Float64()*3, rng.Float64()*2-0.5))
		c.rot[0] = geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: rng.Float64()*180 - 90}
		if k%2 == 0 { // near the cube: up to 25 degrees off its centre
			r, _ := geom.Angles(box.Center().Sub(c.eye[0]))
			c.rot[0] = geom.Rotation{Yaw: r.Yaw + rng.Float64()*50 - 25, Pitch: r.Pitch + rng.Float64()*50 - 25}
		}
		c.nRot = 1
		got := c.rayAngle(box)
		if got > 0 && got < 10 {
			nearMisses++
		}
		e, look := c.eye[0], c.rot[0].Look()
		sampled := math.Inf(1)
		size := box.Size()
		for i := 0; i <= n; i++ {
			for j := 0; j <= n; j++ {
				u, w := float64(i)/n, float64(j)/n
				for _, p := range []geom.Vec3{
					{X: box.Min.X, Y: box.Min.Y + u*size.Y, Z: box.Min.Z + w*size.Z}, {X: box.Max.X, Y: box.Min.Y + u*size.Y, Z: box.Min.Z + w*size.Z},
					{X: box.Min.X + u*size.X, Y: box.Min.Y, Z: box.Min.Z + w*size.Z}, {X: box.Min.X + u*size.X, Y: box.Max.Y, Z: box.Min.Z + w*size.Z},
					{X: box.Min.X + u*size.X, Y: box.Min.Y + w*size.Y, Z: box.Min.Z}, {X: box.Min.X + u*size.X, Y: box.Min.Y + w*size.Y, Z: box.Max.Z},
				} {
					if a, ok := geom.AngleBetween(look, p.Sub(e)); ok {
						sampled = math.Min(sampled, a)
					}
				}
			}
		}
		_, hit := box.IntersectRay(e, look, math.Inf(1))
		if hit != (got == 0) || got > sampled+1e-9 || !hit && sampled-got > 0.5 {
			t.Fatalf("eye %v rot %+v: rayAngle %.4f, sampled %.4f, hit %v", e, c.rot[0], got, sampled, hit)
		}
	}
	if nearMisses < 30 {
		t.Fatalf("fixture: only %d near misses", nearMisses)
	}
}

func TestRayAngleExamples(t *testing.T) {
	box := geom.BlockBox(0, 64, 3) // z 3..4, in front of an eye at the origin column
	var c blockCandidates
	c.addEye(geom.V(0.5, 64.5, 0.5))
	c.rot[0], c.nRot = geom.Rotation{}, 1 // straight at it
	if a := c.rayAngle(box); a != 0 {
		t.Fatalf("a hit: %v", a)
	}
	// Looking west (-x): the nearest direction is to the middle of the cube's edge at x 0, z 3.
	c.rot[0] = geom.Rotation{Yaw: 90}
	if a, want := c.rayAngle(box), math.Atan2(2.5, 0.5)*180/math.Pi; math.Abs(a-want) > 1e-9 {
		t.Fatalf("west: %v, want %v", a, want)
	}
	// 20 degrees west of south passes the cube's west side; the same edge is 11.3 degrees west of south.
	c.rot[1], c.nRot = geom.Rotation{Yaw: 20}, 2
	if a, want := c.rayAngle(box), 20-math.Atan2(0.5, 2.5)*180/math.Pi; math.Abs(a-want) > 1e-9 {
		t.Fatalf("the best of two rotations: %v, want %v", a, want)
	}
}

type nopDigest struct{}

func (nopDigest) Observe(*View, Observation) {}
func (nopDigest) Emitted(*View, string)      {}

// The digest adds no allocation to a judged action.
func TestDigestObservationDoesNotAllocate(t *testing.T) {
	v := NewView()
	v.Protocol = 769
	a := blockAction{eye: geom.V(0.5, 65.62, 0.5), pose: geom.PoseStanding, prevFeet: geom.V(0.3, 64, 0.5), hasPrev: true}
	var c blockCandidates
	c.build(v, &a, &Move{HasPos: true, Pos: geom.V(0.7, 64, 0.5), HasRot: true, Rot: geom.Rotation{Yaw: 40}})
	ctx := NewCtx(v, nil, func(Emission) {})
	ctx.Digest = nopDigest{}
	box := geom.BlockBox(3, 64, 3)
	if n := testing.AllocsPerRun(50, func() {
		o := c.observation(ctx, &a)
		o.Check, o.Failed, o.Value = idScaffoldRay, true, c.rayAngle(box)
		ctx.observe(o)
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// faceMargin > 0 is the face inequality of CHECKS.md 2.12, also at the boundary.
func TestFaceMarginIsTheInequality(t *testing.T) {
	visible := func(face pb.Face, e, b geom.Vec3, u float64) bool {
		switch face {
		case pb.Face_FACE_UP:
			return e.Y > b.Y-u
		case pb.Face_FACE_DOWN:
			return e.Y < b.Y+1+u
		case pb.Face_FACE_EAST:
			return e.X > b.X-u
		case pb.Face_FACE_WEST:
			return e.X < b.X+1+u
		case pb.Face_FACE_SOUTH:
			return e.Z > b.Z-u
		}
		return e.Z < b.Z+1+u
	}
	rng := rand.New(rand.NewPCG(3, 5))
	b := geom.V(10, 64, -7)
	u := 0.03 + geom.EyeUncertainty(769)
	faces := []pb.Face{pb.Face_FACE_UP, pb.Face_FACE_DOWN, pb.Face_FACE_EAST, pb.Face_FACE_WEST, pb.Face_FACE_SOUTH, pb.Face_FACE_NORTH}
	for k := 0; k < 20000; k++ {
		face := faces[k%len(faces)]
		e := b.Add(geom.V(rng.Float64()*4-1.5, rng.Float64()*4-1.5, rng.Float64()*4-1.5))
		if k%3 == 0 { // exactly on the shifted plane
			e = geom.V(b.X-u, b.Y-u, b.Z-u)
			if k%2 == 0 {
				e = geom.V(b.X+1+u, b.Y+1+u, b.Z+1+u)
			}
		}
		if got, want := faceMargin(face, e, b, u) > 0, visible(face, e, b, u); got != want {
			t.Fatalf("face %v eye %v: margin %v, inequality %v", face, e, faceMargin(face, e, b, u), want)
		}
	}
}
