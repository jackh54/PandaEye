package checks_test

import (
	"testing"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

const containerFlag = uint32(pb.MoveFlag_MOVE_FLAG_CONTAINER_OPEN)

// invTick is one client tick of an inventory scenario: clicks (window id, -1 = none) before the movement
// packet, then the move.
type invStep struct {
	window int32   // clicked window, -1 = no click
	open   bool    // the clicked window is the open container (window > 0)
	dh     float64 // horizontal displacement of the move (+z)
	dyaw   float64 // rotation change of the move
	flags  uint32
}

func playInv(r *rig, steps []invStep) {
	for _, s := range steps {
		if s.window >= 0 {
			r.click(s.window, s.open)
		}
		r.flags = s.flags
		rot := geom.Rotation{Yaw: r.p.Rot.Yaw + s.dyaw, Pitch: r.p.Rot.Pitch}
		r.tick(r.p.Pos.Add(geom.V(0, 0, s.dh)), rot)
	}
	r.flags = 0
	r.step(geom.V(0, 0, 0))
	r.step(geom.V(0, 0, 0))
}

// sprintSteps keeps sprinting on the ground at the steady state (h = 0.13 / (1 - 0.546)).
func sprintSteps(n int, window int32, open bool, flags uint32) []invStep {
	out := make([]invStep, n)
	for i := range out {
		out[i] = invStep{window: window, open: open, dh: 0.13 / (1 - 0.546), flags: flags}
	}
	return out
}

func TestInventoryCheatMoveInChest(t *testing.T) {
	r := newRig(t, rigProtocol)
	steps := sprintSteps(5, -1, false, containerFlag) // the chest opened, no clicks yet
	steps = append(steps, sprintSteps(40, 3, true, containerFlag)...)
	playInv(r, steps)
	es := r.run("inventory")
	// 40 click ticks: the first two are never judged, the last needs its successor; 37 judged ticks,
	// a violation every 3.
	expectCount(t, es, "inventory.move", 12)
	expectCount(t, es, "inventory.rotate", 0)
	expectScoring(t, es, "inventory.move", 4, 4, 0.75)
}

func TestInventoryCheatAccelerateFromRest(t *testing.T) {
	// Walking (not sprinting) from rest while clicking in the player's own inventory.
	r := newRig(t, rigProtocol)
	steps := []invStep{{window: -1}, {window: -1}}
	h := 0.0
	for range 12 {
		h = h*0.546 + 0.1
		steps = append(steps, invStep{window: 0, dh: h})
	}
	playInv(r, steps)
	expectAtLeast(t, r.run("inventory"), "inventory.move", 2)
}

func TestInventoryCheatRotate(t *testing.T) {
	for _, c := range []struct {
		name   string
		window int32
		open   bool
		flags  uint32
	}{{"own inventory", 0, false, 0}, {"chest", 7, true, containerFlag}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigProtocol)
			steps := []invStep{{window: -1, flags: c.flags}}
			for range 30 {
				steps = append(steps, invStep{window: c.window, open: c.open, dyaw: 6, flags: c.flags})
			}
			playInv(r, steps)
			es := r.run("inventory")
			expectAtLeast(t, es, "inventory.rotate", 6)
			expectCount(t, es, "inventory.move", 0)
			expectScoring(t, es, "inventory.rotate", 3, 3, 0.7)
		})
	}
}

func TestInventoryLegitClickingWhileDecelerating(t *testing.T) {
	// The chest opens mid-sprint: movement keys are released, the player slides to a halt.
	r := newRig(t, rigProtocol)
	steps := sprintSteps(5, -1, false, 0)
	h := 0.13 / (1 - 0.546)
	for i := range 30 {
		h *= 0.546
		steps = append(steps, invStep{window: 2, open: true, dh: h, flags: containerFlag, dyaw: 0})
		if i%7 == 0 {
			steps[len(steps)-1].dyaw = 0.4 // sub-degree noise
		}
	}
	playInv(r, steps)
	if es := r.run("inventory"); len(es) != 0 {
		t.Fatalf("%+v", es)
	}
}

func TestInventoryLegitGUIPageSwitching(t *testing.T) {
	// A menu plugin switches pages by opening a new window on click. When it closes and reopens, the
	// client grabs the mouse for a moment: rotation and movement input leak into the switch ticks.
	for _, closeFirst := range []bool{false, true} {
		r := newRig(t, rigProtocol)
		steps := []invStep{{window: -1, flags: containerFlag}}
		for page := range int32(10) {
			w := 10 + page
			flags := containerFlag
			if closeFirst {
				flags = 0 // S CLOSE_WINDOW went out before the move, S OPEN_WINDOW after it
			}
			steps = append(steps,
				invStep{window: w, open: true, dyaw: 25, dh: 0.13, flags: flags}, // first click on the new page
				invStep{window: w, open: true, dyaw: 10, dh: 0.2, flags: containerFlag},
				invStep{window: w, open: true, flags: containerFlag},
				invStep{window: w, open: true, flags: containerFlag})
		}
		playInv(r, steps)
		if es := r.run("inventory"); len(es) != 0 {
			t.Fatalf("closeFirst=%v: %+v", closeFirst, es)
		}
	}
}

func TestInventoryLegitCloseInTheSameTickAsAClick(t *testing.T) {
	for _, c := range []struct {
		name   string
		window int32
		open   bool
	}{{"own inventory", 0, false}, {"chest", 5, true}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigProtocol)
			for range 6 {
				var steps []invStep
				flags := uint32(0)
				if c.open {
					flags = containerFlag
				}
				steps = append(steps, invStep{window: -1, flags: flags})
				for range 4 {
					steps = append(steps, invStep{window: c.window, open: c.open, flags: flags})
				}
				// Click, close (E/ESC) and move in the same tick; the C CLOSE_WINDOW cleared the
				// container flag before the movement packet.
				steps = append(steps, invStep{window: c.window, open: c.open, dyaw: 30, dh: 0.13})
				for range 10 {
					steps = append(steps, invStep{window: -1, dyaw: 15, dh: 0.25})
				}
				for _, s := range steps {
					if s.window >= 0 {
						r.click(s.window, s.open)
					}
					r.flags = s.flags
					r.tick(r.p.Pos.Add(geom.V(0, 0, s.dh)), geom.Rotation{Yaw: r.p.Rot.Yaw + s.dyaw})
				}
			}
			r.flags = 0
			r.step(geom.V(0, 0, 0))
			if es := r.run("inventory"); len(es) != 0 {
				t.Fatalf("%+v", es)
			}
		})
	}
}

func TestInventoryNotJudged(t *testing.T) {
	cheat := func(flags uint32) []invStep {
		steps := sprintSteps(3, -1, false, containerFlag|flags)
		for range 30 {
			steps = append(steps, invStep{window: 3, open: true, dh: 0.2863, dyaw: 5, flags: containerFlag | flags})
		}
		return steps
	}
	cases := map[string]func(r *rig) []invStep{
		"server rotation and velocity": func(r *rig) []invStep {
			r.velocity(geom.V(0, 0.4, 0.4))
			return cheat(uint32(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION | pb.MoveFlag_MOVE_FLAG_VELOCITY_PENDING))
		},
		"vehicle": func(r *rig) []invStep { return cheat(uint32(pb.MoveFlag_MOVE_FLAG_IN_VEHICLE)) },
		"water": func(r *rig) []invStep {
			r.env(pb.EnvFlag_ENV_FLAG_IN_WATER)
			return cheat(uint32(pb.MoveFlag_MOVE_FLAG_SERVER_ROTATION))
		},
		"closed window": func(r *rig) []invStep {
			s := cheat(0)
			for i := range s {
				s[i].open = false // clicks on a window that is not open
			}
			return s
		},
		"legacy": func(r *rig) []invStep { return cheat(0) },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			protocol := int32(rigProtocol)
			if name == "legacy" {
				protocol = 340
			}
			r := newRig(t, protocol)
			playInv(r, build(r))
			if es := r.run("inventory"); len(es) != 0 {
				t.Fatalf("%+v", es)
			}
		})
	}
	control := newRig(t, rigProtocol)
	playInv(control, cheat(0))
	es := control.run("inventory")
	expectAtLeast(t, es, "inventory.move", 1)
	expectAtLeast(t, es, "inventory.rotate", 1)
}
