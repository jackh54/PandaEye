// Package geom is the shared geometry of the cloud checks (CHECKS.md 2.1): vectors, axis-aligned
// boxes, ray/box intersection, Minecraft look vectors and angles, eye heights and player boxes.
//
// All angles are in degrees. Functions that can be fed degenerate input (NaN, infinities, zero
// vectors) report it with an ok result instead of returning NaN.
package geom

import "math"

// Vec3 is a point or direction in block units.
type Vec3 struct{ X, Y, Z float64 }

// V returns Vec3{x, y, z}.
func V(x, y, z float64) Vec3 { return Vec3{x, y, z} }

func (a Vec3) Add(b Vec3) Vec3             { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3             { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Scale(s float64) Vec3        { return Vec3{a.X * s, a.Y * s, a.Z * s} }
func (a Vec3) Dot(b Vec3) float64          { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a Vec3) LenSq() float64              { return a.Dot(a) }
func (a Vec3) Len() float64                { return math.Sqrt(a.LenSq()) }
func (a Vec3) Dist(b Vec3) float64         { return a.Sub(b).Len() }
func (a Vec3) DistSq(b Vec3) float64       { return a.Sub(b).LenSq() }
func (a Vec3) HorizontalLen() float64      { return math.Hypot(a.X, a.Z) }
func (a Vec3) Lerp(b Vec3, t float64) Vec3 { return a.Add(b.Sub(a).Scale(t)) }

// Cross returns the cross product a x b.
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

// IsFinite reports whether no component is NaN or infinite.
func (a Vec3) IsFinite() bool { return finite(a.X) && finite(a.Y) && finite(a.Z) }

// Normalize returns a / |a|; ok is false for zero-length or non-finite vectors.
func (a Vec3) Normalize() (Vec3, bool) {
	l := a.Len()
	if l == 0 || !finite(l) {
		return Vec3{}, false
	}
	return a.Scale(1 / l), true
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
