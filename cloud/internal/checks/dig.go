package checks

import (
	"fmt"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// dig.ray (CHECKS.md 2.12b): BlockDig START and FINISH must be in view of some (eye, rotation) candidate,
// with the event's eye and rotation as R0 and the next move as R1 (the scaffold machinery). A miss is no
// candidate ray hitting the block's unit cube expanded by 0.05 within block_reach + 1.5. Violation when
// >= 4 of the last 10 judged digs missed.

const idDigRay = "dig.ray"

func init() {
	Register(Factory{
		Name:   "dig",
		Checks: []string{idDigRay},
		New:    func() Detector { return &digDetector{} },
	})
}

type digDetector struct {
	q      blockQueue
	missed outcomeBits
}

func (d *digDetector) Handle(c *Ctx, ev *pb.Event) {
	if j, ok := ev.GetKind().(*pb.Event_Join); ok && !j.Join.GetUpdate() {
		*d = digDetector{}
		return
	}
	d.q.handle(c, ev, d.judge)
	k, ok := ev.GetKind().(*pb.Event_Dig)
	if !ok {
		return
	}
	dig := k.Dig
	if a := dig.GetAction(); a != pb.BlockDig_ACTION_START && a != pb.BlockDig_ACTION_FINISH {
		return
	}
	b := dig.GetBlock()
	d.q.add(c, blockAction{
		block:    [3]int32{b.GetX(), b.GetY(), b.GetZ()},
		face:     dig.GetFace(),
		eye:      vec3(dig.GetEye()),
		rot:      geom.Rotation{Yaw: float64(dig.GetYaw()), Pitch: float64(dig.GetPitch())},
		sneaking: c.View.Flags&uint32(pb.MoveFlag_MOVE_FLAG_SNEAKING) != 0, // BlockDig has no sneaking field
		reach:    float64(dig.GetBlockReach()),
	})
}

func (d *digDetector) judge(c *Ctx, a *blockAction, cand *blockCandidates) {
	cube := geom.BlockBox(a.block[0], a.block[1], a.block[2])
	miss := !cand.rayHits(cube.Expand(c.Param(idDigRay, "expand", 0.05)), a.reachOr(c.View)+c.Param(idDigRay, "extra_reach", 1.5))
	d.missed.push(miss)
	n := d.missed.count(int(c.Param(idDigRay, "of", 10)))
	if !miss || float64(n) < c.Param(idDigRay, "need", 4) {
		return
	}
	d.missed.reset()
	c.Emit(Emission{Check: idDigRay, Points: 5, Confidence: 0.85, EventTimeUs: a.eventUs,
		Info: fmt.Sprintf("%d of the last digs were not in view of any rotation (last %d,%d,%d)", n, a.block[0], a.block[1], a.block[2]),
		Data: map[string]float64{"missed": float64(n), "x": float64(a.block[0]), "y": float64(a.block[1]), "z": float64(a.block[2])}})
}
