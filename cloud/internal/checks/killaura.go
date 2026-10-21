package checks

import (
	"fmt"
	"slices"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// killaura.hitbox, killaura.multi and killaura.noswing (CHECKS.md 2.3 - 2.5).
//
//   - hitbox: an attack misses when no (eye, rotation) candidate ray hits any target box candidate
//     (expanded by an extra 0.1 for rotation timing); violation when >= 3 of the last 8 judged attacks
//     on players missed. Attacks wait for R1 (PendingR1).
//   - multi (tick clients): attacks on >= 2 distinct entity ids within one client tick.
//   - noswing (tick clients): an INTERACT_ENTITY attack in tick T needs a main-hand swing in tick T,
//     decided once an event of a later tick arrives; violation when >= 3 of the last 10 attacks had
//     none. Swings that other plugins cancelled are still reported by the plugin and count.

// Check ids of the killaura family.
const (
	KillauraHitbox  = "killaura.hitbox"
	KillauraMulti   = "killaura.multi"
	KillauraNoSwing = "killaura.noswing"
)

const (
	hitboxWindow   = 8
	hitboxMisses   = 3
	noswingWindow  = 10
	noswingMissing = 3
)

func init() {
	Register(Factory{Name: "killaura", Checks: []string{KillauraHitbox, KillauraMulti, KillauraNoSwing}, New: func() Detector {
		return &killauraDetector{misses: NewRing[bool](hitboxWindow), noswing: NewRing[bool](noswingWindow)}
	}})
}

type killauraDetector struct {
	pending PendingR1[AttackObs]
	misses  Ring[bool]

	cur           kaTick // the tick being collected (tick clients)
	lastSwingTick uint32 // newest tick with a main-hand swing (0 = none)
	noswing       Ring[bool]
}

// kaTick collects the attacks of one client tick.
type kaTick struct {
	tick     uint32
	attacks  int     // INTERACT_ENTITY attacks (not stabs)
	targets  []int32 // distinct target entity ids
	skipped  bool    // an attack of the tick is not judged (CHECKS.md 2.1)
	pluginUs uint64  // plugin time of the tick's first attack
}

func (d *killauraDetector) Handle(c *Ctx, ev *pb.Event) {
	v := c.View
	for _, e := range d.pending.Resolve(v, ev) {
		d.hitbox(c, e)
	}
	_, quit := ev.GetKind().(*pb.Event_Quit)
	if d.cur.attacks > 0 && (quit || (v.TickClient && ev.GetTick() > d.cur.tick)) {
		d.finishTick(c)
	}
	switch k := ev.GetKind().(type) {
	case *pb.Event_Swing:
		if k.Swing.GetHand() == pb.Hand_HAND_MAIN && ev.GetTick() != 0 {
			d.lastSwingTick = ev.GetTick()
		}
	case *pb.Event_Attack:
		a, _ := NewAttackObs(v, ev)
		if !a.Stab {
			d.pending.Add(v, a, a.R0)
		}
		if !v.TickClient || ev.GetTick() == 0 || a.Stab {
			return
		}
		if d.cur.attacks == 0 || d.cur.tick != ev.GetTick() {
			d.cur = kaTick{tick: ev.GetTick(), pluginUs: ev.GetTimeUs()}
		}
		d.cur.attacks++
		if !slices.Contains(d.cur.targets, a.TargetID) {
			d.cur.targets = append(d.cur.targets, a.TargetID)
		}
		d.cur.skipped = d.cur.skipped || a.SkipReason(false) != ""
	}
}

func (d *killauraDetector) hitbox(c *Ctx, e R1Entry[AttackObs]) {
	miss, ok := HitboxMiss(e)
	if !ok {
		return
	}
	d.misses.Push(miss)
	if !miss {
		return
	}
	n := cwCountTrue(&d.misses)
	if float64(n) < c.Param(KillauraHitbox, "misses", hitboxMisses) {
		return
	}
	c.Emit(Emission{
		Check: KillauraHitbox, Points: 5, Confidence: 0.85, EventTimeUs: uint64(e.Item.PluginUs),
		Info: fmt.Sprintf("attack ray missed the target (%d of the last %d attacks)", n, d.misses.Len()),
		Data: map[string]float64{"misses": float64(n), "attacks": float64(d.misses.Len()), "r0_yaw": e.R0.Yaw,
			"r1_yaw": e.R1.Yaw, "r0_pitch": e.R0.Pitch, "r1_pitch": e.R1.Pitch},
	})
}

// finishTick decides killaura.multi and killaura.noswing for the collected tick.
func (d *killauraDetector) finishTick(c *Ctx) {
	t := d.cur
	d.cur = kaTick{}
	if t.skipped {
		return
	}
	if len(t.targets) >= 2 {
		c.Emit(Emission{
			Check: KillauraMulti, Points: 10, Confidence: 0.95, EventTimeUs: t.pluginUs,
			Info: fmt.Sprintf("%d targets attacked in client tick %d", len(t.targets), t.tick),
			Data: map[string]float64{"targets": float64(len(t.targets)), "attacks": float64(t.attacks)},
		})
	}
	swung := d.lastSwingTick == t.tick
	for i := 0; i < t.attacks; i++ {
		d.noswing.Push(!swung)
	}
	if swung {
		return
	}
	n := cwCountTrue(&d.noswing)
	if float64(n) < c.Param(KillauraNoSwing, "missing", noswingMissing) {
		return
	}
	c.Emit(Emission{
		Check: KillauraNoSwing, Points: 6, Confidence: 0.9, EventTimeUs: t.pluginUs,
		Info: fmt.Sprintf("attack without a swing in client tick %d (%d of the last %d attacks)", t.tick, n, d.noswing.Len()),
		Data: map[string]float64{"missing": float64(n), "attacks": float64(d.noswing.Len())},
	})
}
