package geom

import "math"

// AABB is an axis-aligned box with Min <= Max on every axis.
type AABB struct{ Min, Max Vec3 }

// Box returns the box spanned by two corners in any order.
func Box(a, b Vec3) AABB {
	return AABB{
		Min: Vec3{math.Min(a.X, b.X), math.Min(a.Y, b.Y), math.Min(a.Z, b.Z)},
		Max: Vec3{math.Max(a.X, b.X), math.Max(a.Y, b.Y), math.Max(a.Z, b.Z)},
	}
}

// EntityBox returns the box of an entity standing at feet (bottom centre) with the given width and
// height.
func EntityBox(feet Vec3, width, height float64) AABB {
	h := width / 2
	return AABB{Min: Vec3{feet.X - h, feet.Y, feet.Z - h}, Max: Vec3{feet.X + h, feet.Y + height, feet.Z + h}}
}

// BlockBox returns the unit cube of the block at integer coordinates (x, y, z).
func BlockBox(x, y, z int32) AABB {
	m := Vec3{float64(x), float64(y), float64(z)}
	return AABB{Min: m, Max: m.Add(Vec3{1, 1, 1})}
}

// Expand grows the box by d on every side (shrinks for negative d, never past its centre).
func (b AABB) Expand(d float64) AABB { return b.ExpandXYZ(d, d, d) }

// ExpandXYZ grows the box by dx, dy, dz on the respective sides.
func (b AABB) ExpandXYZ(dx, dy, dz float64) AABB {
	d := Vec3{dx, dy, dz}
	out := AABB{Min: b.Min.Sub(d), Max: b.Max.Add(d)}
	c := b.Center()
	if out.Min.X > out.Max.X {
		out.Min.X, out.Max.X = c.X, c.X
	}
	if out.Min.Y > out.Max.Y {
		out.Min.Y, out.Max.Y = c.Y, c.Y
	}
	if out.Min.Z > out.Max.Z {
		out.Min.Z, out.Max.Z = c.Z, c.Z
	}
	return out
}

// Offset moves the box by v.
func (b AABB) Offset(v Vec3) AABB { return AABB{Min: b.Min.Add(v), Max: b.Max.Add(v)} }

// Center returns the centre of the box.
func (b AABB) Center() Vec3 { return b.Min.Add(b.Max).Scale(0.5) }

// Size returns the edge lengths of the box.
func (b AABB) Size() Vec3 { return b.Max.Sub(b.Min) }

// Union returns the smallest box containing b and o.
func (b AABB) Union(o AABB) AABB {
	return AABB{
		Min: Vec3{math.Min(b.Min.X, o.Min.X), math.Min(b.Min.Y, o.Min.Y), math.Min(b.Min.Z, o.Min.Z)},
		Max: Vec3{math.Max(b.Max.X, o.Max.X), math.Max(b.Max.Y, o.Max.Y), math.Max(b.Max.Z, o.Max.Z)},
	}
}

// IsFinite reports whether both corners are finite.
func (b AABB) IsFinite() bool { return b.Min.IsFinite() && b.Max.IsFinite() }

// Contains reports whether p lies inside the box or on its surface.
func (b AABB) Contains(p Vec3) bool {
	return p.X >= b.Min.X && p.X <= b.Max.X &&
		p.Y >= b.Min.Y && p.Y <= b.Max.Y &&
		p.Z >= b.Min.Z && p.Z <= b.Max.Z
}

// Intersects reports whether the boxes overlap or touch.
func (b AABB) Intersects(o AABB) bool {
	return b.Min.X <= o.Max.X && b.Max.X >= o.Min.X &&
		b.Min.Y <= o.Max.Y && b.Max.Y >= o.Min.Y &&
		b.Min.Z <= o.Max.Z && b.Max.Z >= o.Min.Z
}

// ClosestPoint returns the point of the box closest to p.
func (b AABB) ClosestPoint(p Vec3) Vec3 {
	return Vec3{clamp(p.X, b.Min.X, b.Max.X), clamp(p.Y, b.Min.Y, b.Max.Y), clamp(p.Z, b.Min.Z, b.Max.Z)}
}

// DistanceTo returns the Euclidean distance from p to the box (0 when p is inside). It returns NaN
// only for non-finite input.
func (b AABB) DistanceTo(p Vec3) float64 { return p.Dist(b.ClosestPoint(p)) }

// IntersectRay intersects the ray origin + t*dir (t >= 0) with the box using the slab method. dir
// need not be normalised; the returned t is the distance from origin to the entry point (0 when the
// origin is inside the box). Hits farther than maxDist are misses; pass math.Inf(1) for no limit.
// Degenerate input (non-finite values, zero-length dir with the origin outside) never hits.
func (b AABB) IntersectRay(origin, dir Vec3, maxDist float64) (t float64, hit bool) {
	if !origin.IsFinite() || !b.IsFinite() || math.IsNaN(maxDist) || maxDist < 0 {
		return 0, false
	}
	if b.Contains(origin) {
		return 0, true
	}
	d, ok := dir.Normalize()
	if !ok {
		return 0, false
	}
	tMin, tMax := 0.0, math.Inf(1)
	for axis := 0; axis < 3; axis++ {
		o, v, lo, hi := comp(origin, axis), comp(d, axis), comp(b.Min, axis), comp(b.Max, axis)
		if v == 0 {
			if o < lo || o > hi {
				return 0, false
			}
			continue
		}
		t1, t2 := (lo-o)/v, (hi-o)/v
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin = math.Max(tMin, t1)
		tMax = math.Min(tMax, t2)
		if tMin > tMax {
			return 0, false
		}
	}
	if tMin > maxDist {
		return 0, false
	}
	return tMin, true
}

func comp(v Vec3, axis int) float64 {
	switch axis {
	case 0:
		return v.X
	case 1:
		return v.Y
	default:
		return v.Z
	}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
