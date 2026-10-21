package checks

import (
	"fmt"
	"math"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// inventory.move and inventory.rotate (CHECKS.md 2.13). Tick clients only.
//
// A client tick t (events with Event.tick == t) is "screen open" when it and tick t-1 both have a window
// click (window 0 or the open container) and neither has an open or a close. The wire protocol carries no
// OPEN_WINDOW/CLOSE_WINDOW events, so they are inferred conservatively from what the cloud sees:
//   - a click on another window than the previous click (S OPEN_WINDOW: GUI page switches reopen with a
//     new id), or on a window that is not open;
//   - a change of MOVE_FLAG_CONTAINER_OPEN across the moves of ticks t-1 and t (and the move before them),
//     or a flag that contradicts the clicked window (container clicks need it, window 0 clicks must not
//     have it): S OPEN_WINDOW, S CLOSE_WINDOW and C CLOSE_WINDOW of containers all change it;
//   - the player's own inventory (window 0) is opened and closed client-side without a container flag,
//     so a tick with window-0 clicks also needs a click in tick t+1 (proof it was not closed in tick t).
// Tick t is therefore judged once tick t+1 is complete (an event of tick >= t+2 arrived).

const (
	idInventoryMove   = "inventory.move"
	idInventoryRotate = "inventory.rotate"
)

func init() {
	Register(Factory{
		Name:   "inventory",
		Checks: []string{idInventoryMove, idInventoryRotate},
		New:    func() Detector { return &inventoryDetector{lastWindow: -1} },
	})
}

const invWindowTicks = 100 // 5 s

// envExclusionsInventory: water/lava/ice/bubble/vehicle/gliding (CHECKS.md 2.13) plus entity pushing and
// riptide, which also accelerate a player without movement input.
var envExclusionsInventory = []pb.EnvFlag{
	pb.EnvFlag_ENV_FLAG_IN_WATER, pb.EnvFlag_ENV_FLAG_IN_LAVA, pb.EnvFlag_ENV_FLAG_ON_ICE,
	pb.EnvFlag_ENV_FLAG_IN_BUBBLE_COLUMN, pb.EnvFlag_ENV_FLAG_IN_VEHICLE, pb.EnvFlag_ENV_FLAG_GLIDING,
	pb.EnvFlag_ENV_FLAG_NEAR_ENTITY, pb.EnvFlag_ENV_FLAG_RIPTIDING,
}

const invMoveExclusions = uint32(pb.MoveFlag_MOVE_FLAG_VELOCITY_PENDING | pb.MoveFlag_MOVE_FLAG_GLIDING |
	pb.MoveFlag_MOVE_FLAG_IN_VEHICLE | pb.MoveFlag_MOVE_FLAG_SWIMMING | pb.MoveFlag_MOVE_FLAG_RIPTIDE |
	pb.MoveFlag_MOVE_FLAG_TELEPORT_PENDING | pb.MoveFlag_MOVE_FLAG_FLYING)

// invTick is what one client tick contained.
type invTick struct {
	tick     uint32
	clicks   int   // clicks on window 0 or the open container
	window   int32 // window of the newest such click
	win0     bool
	badClick bool // a click on a window that is not open
	switched bool // the clicked window changed (an open happened)
}

type inventoryDetector struct {
	env        envClock
	cur        invTick
	done       [4]invTick // the newest completed ticks with clicks, oldest first
	evaluated  uint32
	lastWindow int32 // window of the newest click, -1 = none
	moves      timeMarks
	rots       timeMarks
}

func (d *inventoryDetector) Handle(c *Ctx, ev *pb.Event) {
	v := c.View
	if j, ok := ev.GetKind().(*pb.Event_Join); ok && !j.Join.GetUpdate() {
		*d = inventoryDetector{lastWindow: -1}
		return
	}
	d.env.observe(v, ev)
	if !v.TickClient {
		return
	}
	t := ev.GetTick()
	if t == 0 {
		return
	}
	if t > d.cur.tick {
		if d.cur.clicks > 0 || d.cur.badClick {
			copy(d.done[:], d.done[1:])
			d.done[len(d.done)-1] = d.cur
		}
		for _, g := range d.done {
			if g.clicks > 0 && g.tick > d.evaluated && g.tick+1 < t {
				d.evaluated = g.tick
				d.evaluate(c, g.tick)
			}
		}
		d.cur = invTick{tick: t}
	}
	if k, ok := ev.GetKind().(*pb.Event_Click); ok && t == d.cur.tick {
		cl := k.Click
		w := cl.GetWindowId()
		if w != 0 && !cl.GetContainerOpen() {
			d.cur.badClick = true
			d.lastWindow = -1
			return
		}
		if w != d.lastWindow {
			d.cur.switched = true
		}
		d.lastWindow = w
		d.cur.clicks++
		d.cur.window = w
		d.cur.win0 = d.cur.win0 || w == 0
	}
}

func (d *inventoryDetector) group(tick uint32) invTick {
	for _, g := range d.done {
		if g.tick == tick && tick != 0 {
			return g
		}
	}
	return invTick{tick: tick}
}

// evaluate judges tick e, whose successor tick is complete.
func (d *inventoryDetector) evaluate(c *Ctx, e uint32) {
	v := c.View
	if !v.Judgeable() || v.InVehicle {
		return
	}
	g, p, n := d.group(e), d.group(e-1), d.group(e+1)
	if g.clicks == 0 || p.clicks == 0 || g.badClick || p.badClick || g.switched || p.switched || g.window != p.window {
		return
	}
	if (g.win0 || p.win0) && n.clicks == 0 {
		return
	}
	// The moves of ticks e-1 and e, and the move before them.
	hi, lo := -1, -1
	for i := v.Moves.Len() - 1; i >= 0; i-- {
		m := v.Moves.At(i)
		if m.Tick > e {
			continue
		}
		if m.Tick < e-1 {
			break
		}
		if hi < 0 {
			hi = i
		}
		lo = i
	}
	if hi < 0 || lo < 1 {
		return // no move to judge, or nothing to compare the first one with
	}
	container := g.window != 0
	for i := lo - 1; i <= hi; i++ {
		if v.Moves.At(i).Has(pb.MoveFlag_MOVE_FLAG_CONTAINER_OPEN) != container {
			return // a container was opened or closed around these ticks
		}
	}
	moved, rotated := false, 0.0
	var movedH float64
	for i := max(lo, 1); i <= hi; i++ {
		m := v.Moves.At(i)
		if m.Tick != e {
			continue
		}
		prev := v.Moves.At(i - 1)
		// Both rotations must have been sent with the screen open: the previous one in tick e-1 or e.
		if m.HasRot && prev.Tick >= e-1 && !m.Has(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION) &&
			!m.Has(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE) && m.TimeUs-v.LastTeleportUs > time1s {
			rotated = math.Max(rotated, rotationChange(prev.Rot, m.Rot))
		}
		if i >= 2 && !moved {
			if h, ok := d.accelerating(c, v.Moves.At(i-2), prev, m); ok {
				moved, movedH = true, h
			}
		}
	}

	if moved {
		d.moves.add(int64(e))
		need := c.Param(idInventoryMove, "need", 3)
		if cnt := d.moves.within(int64(e), invWindowTicks); float64(cnt) >= need {
			d.moves.reset()
			c.Emit(Emission{Check: idInventoryMove, Points: 4, Confidence: 0.75,
				Info: fmt.Sprintf("accelerated with an inventory open in %d ticks within 5 s (h %.3f)", cnt, movedH),
				Data: map[string]float64{"ticks": float64(cnt), "h": movedH}})
		}
	}
	if rotated > c.Param(idInventoryRotate, "min_deg", 1) {
		d.rots.add(int64(e))
		need := c.Param(idInventoryRotate, "need", 4)
		if cnt := d.rots.within(int64(e), invWindowTicks); float64(cnt) >= need {
			d.rots.reset()
			c.Emit(Emission{Check: idInventoryRotate, Points: 3, Confidence: 0.7,
				Info: fmt.Sprintf("rotated with an inventory open in %d ticks within 5 s (%.1f degrees)", cnt, rotated),
				Data: map[string]float64{"ticks": float64(cnt), "deg": rotated}})
		}
	}
}

// accelerating applies the inventory.move movement rule to the consecutive moves pp -> p -> m.
func (d *inventoryDetector) accelerating(c *Ctx, pp, p, m Move) (float64, bool) {
	v := c.View
	if !Consecutive(p, m, true) || !Consecutive(pp, p, true) || m.Flags&invMoveExclusions != 0 || p.Flags&invMoveExclusions != 0 {
		return 0, false
	}
	if m.TimeUs-v.LastVelocityUs <= time1s && v.LastVelocityUs != 0 || m.TimeUs-v.LastExplosionUs <= time1s && v.LastExplosionUs != 0 ||
		m.TimeUs-v.LastTeleportUs <= time1s && v.LastTeleportUs != 0 {
		return 0, false
	}
	for _, f := range envExclusionsInventory {
		if d.env.recently(v, f, time1s) {
			return 0, false
		}
	}
	h := m.Pos.Sub(p.Pos).HorizontalLen()
	hPrev := p.Pos.Sub(pp.Pos).HorizontalLen()
	minH := c.Param(idInventoryMove, "min_h", 0.1)
	if h > minH && h > hPrev*0.91+c.Param(idInventoryMove, "accel", 0.02) {
		return h, true
	}
	return 0, false
}

// rotationChange is the larger of the yaw (shortest path) and pitch changes, in degrees.
func rotationChange(a, b geom.Rotation) float64 {
	return math.Max(math.Abs(geom.YawDelta(a.Yaw, b.Yaw)), math.Abs(b.Pitch-a.Pitch))
}
