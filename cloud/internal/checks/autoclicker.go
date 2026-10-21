package checks

import (
	"fmt"
	"math"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// autoclicker.consistency, autoclicker.pattern and autoclicker.cps (CHECKS.md 2.11). Tick clients only.
//
// Clicks are main-hand swings counted per client tick (the tick number of the event), within 1.5 s
// (30 ticks) before or after an attack by the player. A tick's swings are dropped when the tick also has a
// block placement or a use item, or when it lies between a dig START and its CANCEL/FINISH plus 6 ticks.
// The tick is decided once an event of a later tick arrives; ticks that are not yet within 30 ticks of an
// attack wait (bounded) for one. Counted clicks fill windows of 40 clicks spanning <= 5 s (100 ticks);
// clicks that would make a window longer push its oldest clicks out. Each full window is judged once.

const (
	idAutoclickerConsistency = "autoclicker.consistency"
	idAutoclickerPattern     = "autoclicker.pattern"
	idAutoclickerCPS         = "autoclicker.cps"
)

func init() {
	Register(Factory{
		Name:   "autoclicker",
		Checks: []string{idAutoclickerConsistency, idAutoclickerPattern, idAutoclickerCPS},
		New:    func() Detector { return &autoclickerDetector{} },
	})
}

const (
	acAttackTicks  = 30  // 1.5 s
	acDigTailTicks = 6   // swings this long after a dig's CANCEL/FINISH are still digging swings
	acMaxSpanTicks = 100 // 5 s
	acMaxWindow    = 256 // upper bound of the window parameter
)

type acTick struct {
	tick   uint32
	clicks int
}

type autoclickerDetector struct {
	// The tick being collected.
	tick     uint32
	swings   int
	excluded bool // placement, use item or dig event in this tick

	digOpen    bool
	digUntil   uint32 // swings up to this tick (inclusive) belong to a dig
	attackTick uint32 // newest attack, 0 = none

	pending []acTick // decided click ticks waiting for an attack within 30 ticks (<= 31 entries)
	window  []uint32 // click ticks of the current window, oldest first

	// For autoclicker.cps: the previous window and whether the current one directly follows it.
	prevHigh   bool
	prevCPS    float64
	contiguous bool
}

func (d *autoclickerDetector) Handle(c *Ctx, ev *pb.Event) {
	v := c.View
	if j, ok := ev.GetKind().(*pb.Event_Join); ok && !j.Join.GetUpdate() {
		*d = autoclickerDetector{}
		return
	}
	if !v.TickClient {
		return
	}
	t := ev.GetTick()
	if t == 0 {
		return // server-level or pre-tick events
	}
	if t != d.tick {
		d.closeTick(c)
		d.tick, d.swings, d.excluded = t, 0, false
	}
	switch k := ev.GetKind().(type) {
	case *pb.Event_Swing:
		if k.Swing.GetHand() == pb.Hand_HAND_MAIN {
			d.swings++
		}
	case *pb.Event_Place, *pb.Event_UseItem:
		d.excluded = true
	case *pb.Event_Dig:
		d.excluded = true
		switch k.Dig.GetAction() {
		case pb.BlockDig_ACTION_START:
			d.digOpen = true
		case pb.BlockDig_ACTION_CANCEL, pb.BlockDig_ACTION_FINISH:
			d.endDig(t)
		}
	case *pb.Event_Attack:
		d.onAttack(c, t)
	}
}

// endDig closes an open dig at tick t; its swings keep being excluded for 6 more ticks.
func (d *autoclickerDetector) endDig(t uint32) {
	d.digOpen = false
	d.digUntil = max(d.digUntil, t+acDigTailTicks)
}

func (d *autoclickerDetector) onAttack(c *Ctx, t uint32) {
	d.attackTick = t
	// A vanilla client aborts any dig before it can attack, and an instantly broken block never gets a
	// CANCEL/FINISH: an attack therefore ends the dig exclusion (plus its 6-tick tail).
	if d.digOpen {
		d.endDig(t)
	}
	for _, p := range d.pending {
		if t-p.tick <= acAttackTicks {
			d.commit(c, p.tick, p.clicks)
		}
	}
	d.pending = d.pending[:0]
}

// closeTick decides the swings of the collected tick.
func (d *autoclickerDetector) closeTick(c *Ctx) {
	if d.tick == 0 {
		return
	}
	// Ticks that can no longer get an attack within 30 ticks are not clicks.
	drop := 0
	for drop < len(d.pending) && d.tick-d.pending[drop].tick > acAttackTicks {
		drop++
	}
	d.pending = d.pending[:copy(d.pending, d.pending[drop:])]
	if d.swings == 0 || d.excluded || d.digOpen || d.tick <= d.digUntil || !c.View.Judgeable() {
		return
	}
	if d.attackTick != 0 && d.tick-d.attackTick <= acAttackTicks {
		d.commit(c, d.tick, d.swings)
		return
	}
	d.pending = append(d.pending, acTick{d.tick, d.swings})
}

func (d *autoclickerDetector) commit(c *Ctx, tick uint32, clicks int) {
	size := int(Clamp(c.Param(idAutoclickerConsistency, "window", 40), 10, acMaxWindow))
	for range clicks {
		drop := 0
		for drop < len(d.window) && tick-d.window[drop] > acMaxSpanTicks {
			drop++
		}
		if drop > 0 {
			d.window = append(d.window[:0], d.window[drop:]...)
			d.contiguous = false
		}
		d.window = append(d.window, tick)
		if len(d.window) >= size {
			d.evaluate(c)
			d.window = d.window[:0]
		}
	}
}

func (d *autoclickerDetector) evaluate(c *Ctx) {
	w := d.window
	n := len(w)
	span := float64(w[n-1] - w[0])
	cps := float64(n-1) * 20 / math.Max(span, 1) // intervals per second of client ticks
	iv := make([]float64, n-1)
	for i := range iv {
		iv[i] = float64(w[i+1] - w[i])
	}
	mode, share := ModeShare(iv)
	sd := StdDev(iv)
	data := func() map[string]float64 {
		return map[string]float64{"cps": cps, "mode_interval": mode, "mode_share": share, "sd": sd, "clicks": float64(n)}
	}

	if cps >= c.Param(idAutoclickerConsistency, "min_cps", 8) && cps <= c.Param(idAutoclickerConsistency, "max_cps", 16) &&
		share >= c.Param(idAutoclickerConsistency, "share", 0.95) && sd < c.Param(idAutoclickerConsistency, "max_sd", 0.25) {
		c.Emit(Emission{Check: idAutoclickerConsistency, Points: 4, Confidence: 0.7, Data: data(),
			Info: fmt.Sprintf("%.1f CPS, %.0f%% of intervals %g ticks (sd %.2f)", cps, 100*share, mode, sd)})
	}

	if cps >= c.Param(idAutoclickerPattern, "min_cps", 8) && cps <= c.Param(idAutoclickerPattern, "max_cps", 16) && Distinct(iv) >= 2 {
		if period, explained := repeatingPattern(iv, 2, 4); explained >= c.Param(idAutoclickerPattern, "share", 0.9) {
			e := Emission{Check: idAutoclickerPattern, Points: 4, Confidence: 0.7, Data: data(),
				Info: fmt.Sprintf("%.1f CPS, a period-%d interval pattern explains %.0f%%", cps, period, 100*explained)}
			e.Data["period"], e.Data["explained"] = float64(period), explained
			c.Emit(e)
		}
	}

	limit := c.Param(idAutoclickerCPS, "limit", 25)
	high := cps >= limit
	if high && d.prevHigh && d.contiguous {
		low := math.Min(cps, d.prevCPS)
		e := Emission{Check: idAutoclickerCPS, Points: 2 + math.Min(6, (low-limit)*0.5), Confidence: 0.6, Data: data(),
			Info: fmt.Sprintf("%.1f and %.1f CPS in two consecutive windows (limit %g)", d.prevCPS, cps, limit)}
		e.Data["limit"] = limit
		c.Emit(e)
	}
	d.prevHigh, d.prevCPS, d.contiguous = high, cps, true
}

// repeatingPattern finds the period p in [minP, maxP] whose repeating sequence (the most frequent interval
// at each phase i mod p) explains the largest share of iv. Sequences that are constant (a single
// interval, which is autoclicker.consistency's domain) do not count.
func repeatingPattern(iv []float64, minP, maxP int) (period int, explained float64) {
	for p := minP; p <= maxP; p++ {
		if len(iv) < 2*p {
			continue
		}
		seq := make([]float64, p)
		hits := 0
		for phase := range p {
			var col []float64
			for i := phase; i < len(iv); i += p {
				col = append(col, iv[i])
			}
			m, share := ModeShare(col)
			seq[phase] = m
			hits += int(math.Round(share * float64(len(col))))
		}
		if Distinct(seq) < 2 {
			continue
		}
		if e := float64(hits) / float64(len(iv)); e > explained {
			period, explained = p, e
		}
	}
	return period, explained
}
