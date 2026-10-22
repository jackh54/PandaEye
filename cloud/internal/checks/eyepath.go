package checks

import "github.com/jackh54/PandaEye/cloud/internal/geom"

// Since 1.20 the client picks its block and entity target every frame, from getEyePosition(partialTick):
// the eye lies between the position before the last movement packet and the last one, not at the last
// one. Clicks and attacks are sent in the next tick with the last frame's pick, so a player moving fast
// (sprinting, /fly, elytra) clicks from up to one tick of movement behind the reported eye.

// eyePathSamples is the number of points tried between the previous and the last position (the last
// one excluded). 8 keeps the sampling error below 1/16 of a tick's movement.
const eyePathSamples = 8

// eyePathMaxAgeUs: the previous position only matters when the last one arrived just before the action
// (the player moved in the tick before); a stationary player's eye is the last position.
const eyePathMaxAgeUs = 150_000

// eyePathMaxStep bounds the segment; longer steps are teleports or corrections, not movement.
const eyePathMaxStep = 4.0

// PrevFeet returns the position received before the last one, when the last one arrived within
// eyePathMaxAgeUs and the step between them is movement.
func (v *View) PrevFeet() (geom.Vec3, bool) {
	var cur *Move
	for k := 0; k < v.Moves.Len(); k++ {
		m, _ := v.Moves.FromEnd(k)
		if !m.HasPos {
			continue
		}
		if cur == nil {
			if v.NowUs-m.TimeUs > eyePathMaxAgeUs {
				return geom.Vec3{}, false
			}
			c := m
			cur = &c
			continue
		}
		if m.Pos.Dist(cur.Pos) > eyePathMaxStep {
			return geom.Vec3{}, false
		}
		return m.Pos, true
	}
	return geom.Vec3{}, false
}

// eyePath returns the eyes between prevFeet and feet at height h, feet itself excluded.
func eyePath(prevFeet, feet geom.Vec3, h float64) []geom.Vec3 {
	out := make([]geom.Vec3, 0, eyePathSamples)
	step := feet.Sub(prevFeet)
	for k := 0; k < eyePathSamples; k++ {
		t := float64(k) / eyePathSamples
		out = append(out, prevFeet.Add(step.Scale(t)).Add(geom.V(0, h, 0)))
	}
	return out
}
