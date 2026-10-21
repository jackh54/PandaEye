package geom

import "math"

const degToRad = math.Pi / 180

// Rotation is a yaw/pitch pair in degrees as sent by the client (yaw unwrapped).
type Rotation struct{ Yaw, Pitch float64 }

// LookVector returns the unit view direction of the Minecraft rotation (yaw, pitch):
// x = -sin(yaw)*cos(pitch), y = -sin(pitch), z = cos(yaw)*cos(pitch).
func LookVector(yaw, pitch float64) Vec3 {
	y, p := yaw*degToRad, pitch*degToRad
	cp := math.Cos(p)
	return Vec3{-math.Sin(y) * cp, -math.Sin(p), math.Cos(y) * cp}
}

// Look returns LookVector(r.Yaw, r.Pitch).
func (r Rotation) Look() Vec3 { return LookVector(r.Yaw, r.Pitch) }

// Angles returns the rotation whose look vector points along dir: yaw in [-180, 180), pitch in
// [-90, 90]. ok is false for zero-length or non-finite dir.
func Angles(dir Vec3) (r Rotation, ok bool) {
	d, ok := dir.Normalize()
	if !ok {
		return Rotation{}, false
	}
	pitch := -math.Asin(clamp(d.Y, -1, 1)) / degToRad
	yaw := 0.0
	if d.X != 0 || d.Z != 0 {
		yaw = WrapDegrees(math.Atan2(-d.X, d.Z) / degToRad)
	}
	return Rotation{Yaw: yaw, Pitch: pitch}, true
}

// AngleBetween returns the angle between a and b in degrees (0..180). ok is false when either vector
// has zero length or is not finite.
func AngleBetween(a, b Vec3) (deg float64, ok bool) {
	if !a.IsFinite() || !b.IsFinite() || a.LenSq() == 0 || b.LenSq() == 0 {
		return 0, false
	}
	// atan2(|a x b|, a.b) stays accurate for tiny and near-180 degree angles, unlike acos.
	return math.Atan2(a.Cross(b).Len(), a.Dot(b)) / degToRad, true
}

// WrapDegrees maps an angle to [-180, 180). Non-finite input returns NaN.
func WrapDegrees(a float64) float64 {
	if !finite(a) {
		return math.NaN()
	}
	a = math.Mod(a+180, 360)
	if a < 0 {
		a += 360
	}
	return a - 180
}

// YawDelta returns the signed shortest rotation from `from` to `to`, in [-180, 180).
func YawDelta(from, to float64) float64 { return WrapDegrees(to - from) }

// LerpYaw interpolates from a to b along the shortest angular path: t=0 gives a, t=1 gives a plus the
// shortest delta (equal to b modulo 360). The result is not wrapped, matching unwrapped client yaws.
func LerpYaw(a, b, t float64) float64 { return a + YawDelta(a, b)*t }

// Lerp interpolates two rotations: yaw along the shortest path, pitch linearly.
func (r Rotation) Lerp(to Rotation, t float64) Rotation {
	return Rotation{Yaw: LerpYaw(r.Yaw, to.Yaw, t), Pitch: r.Pitch + (to.Pitch-r.Pitch)*t}
}

// RotationCandidates returns r0, the interpolations r0->r1 at 0.25, 0.5 and 0.75 (shortest angular
// path) and r1 (CHECKS.md 2.1).
func RotationCandidates(r0, r1 Rotation) []Rotation {
	return []Rotation{r0, r0.Lerp(r1, 0.25), r0.Lerp(r1, 0.5), r0.Lerp(r1, 0.75), r1}
}
