package geom

import "math"

// Client protocol numbers that change geometry.
const (
	Protocol1_14   = 477 // sneaking eye height 1.27 from here on (1.54 before)
	Protocol1_18_2 = 758 // eye position precision improves from here on
	Protocol1_21_2 = 768 // "tick clients": CLIENT_TICK_END, one movement packet per tick
)

// Player hitbox (CHECKS.md 2.1), before scale.
const (
	PlayerWidth  = 0.6
	PlayerHeight = 1.8
)

// Pose selects the eye height.
type Pose int

const (
	PoseStanding Pose = iota
	PoseSneaking
	PoseSwimming
	PoseGliding
	PoseCrawling
)

// EyeHeight returns the eye height for a pose, client protocol and scale attribute: standing 1.62,
// sneaking 1.27 (1.54 for clients before 1.14), swimming/gliding/crawling 0.4, times scale. An unknown
// protocol (<= 0) is treated as current; a non-positive or non-finite scale as 1.
func EyeHeight(pose Pose, protocol int32, scale float64) float64 {
	h := 1.62
	switch pose {
	case PoseSneaking:
		h = 1.27
		if protocol > 0 && protocol < Protocol1_14 {
			h = 1.54
		}
	case PoseSwimming, PoseGliding, PoseCrawling:
		h = 0.4
	}
	return h * normScale(scale)
}

// EyeUncertainty returns the eye position uncertainty: 0.03 for clients before 1.18.2 (and unknown
// protocols, the lenient choice), 0.0002 after.
func EyeUncertainty(protocol int32) float64 {
	if protocol >= Protocol1_18_2 {
		return 0.0002
	}
	return 0.03
}

// PlayerBox returns the player hitbox at feet for the scale attribute. Target boxes always use this
// standing box, the largest a player can have (superset).
func PlayerBox(feet Vec3, scale float64) AABB {
	s := normScale(scale)
	return EntityBox(feet, PlayerWidth*s, PlayerHeight*s)
}

// Eye returns the eye position for feet, pose, protocol and scale.
func Eye(feet Vec3, pose Pose, protocol int32, scale float64) Vec3 {
	return Vec3{feet.X, feet.Y + EyeHeight(pose, protocol, scale), feet.Z}
}

func normScale(s float64) float64 {
	if !(s > 0) || math.IsInf(s, 0) {
		return 1
	}
	return s
}
