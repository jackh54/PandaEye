package checks

import (
	"fmt"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// latency.pingspoof (CHECKS.md 2.14) and latency.blink (2.15).
//
// pingspoof: the plugin already drops keep-alive samples whose id was sent more than once within 30 s
// (plugin-injected keep-alives, AckTracker "injected"), so every matched keep-alive sample here is clean.
// Unmatched responses score 0 points in v1 and are ignored. Each keep-alive waits until 2 s after it, is
// paired with the median of the pong samples within +-2 s, and is in excess when its RTT exceeds the
// paired pong by > 150 ms and by > 1.6x. Violation when >= 3 of the last 4 paired keep-alives are in
// excess (at least 4 paired), at most once per 60 s.
//
// blink (tick clients): a gap > 500 ms between consecutive client-originated events, followed by >= 10
// client ticks arriving within 100 ms, while the player attacked in the 3 s before the gap or during the
// burst. Ticks "arrive" with events: the burst counts the tick advance across the events of its first
// 100 ms, the first event contributing at most one tick (an idle tick client sends one movement packet per
// second, so its first event after a pause legitimately carries many ticks). The burst is confirmed 1 s
// later, once a ServerStall covering the gap would have been reported. Violation when >= 3 blinks in 60 s.

const (
	idLatencyPingspoof = "latency.pingspoof"
	idLatencyBlink     = "latency.blink"
)

func init() {
	Register(Factory{
		Name:   "latency",
		Checks: []string{idLatencyPingspoof, idLatencyBlink},
		New:    func() Detector { return &latencyDetector{} },
	})
}

const (
	latPongsKept     = 128
	latKeepAliveKept = 8
)

type latSample struct {
	atUs  int64
	rttMs float64
}

type latencyDetector struct {
	pongs      Ring[latSample]
	keepAlives Ring[latSample] // waiting to be paired
	paired     outcomeBits     // true = excess
	lastPingUs int64           // last pingspoof violation

	blink blinkTracker
}

type blinkTracker struct {
	lastUs     int64 // newest client-originated event
	lastTick   uint32
	attackUs   int64 // newest attack by the player
	collecting bool  // inside the first 100 ms after a gap
	gapStartUs int64
	burstUs    int64 // first event after the gap
	baseTick   uint32
	minTick    uint32
	maxTick    uint32
	attacked   bool // attacked before the gap or during the burst
	confirmAt  int64
	confirming bool
	ticks      uint32
	marks      timeMarks
}

func (d *latencyDetector) Handle(c *Ctx, ev *pb.Event) {
	if j, ok := ev.GetKind().(*pb.Event_Join); ok && !j.Join.GetUpdate() {
		*d = latencyDetector{}
		return
	}
	d.pingspoof(c, ev)
	d.blink.handle(c, ev)
}

// ---------------------------------------------------------------------------------------------
// latency.pingspoof
// ---------------------------------------------------------------------------------------------

func (d *latencyDetector) pingspoof(c *Ctx, ev *pb.Event) {
	v := c.View
	if d.pongs.Cap() <= 1 {
		d.pongs, d.keepAlives = NewRing[latSample](latPongsKept), NewRing[latSample](latKeepAliveKept)
	}
	if k, ok := ev.GetKind().(*pb.Event_Latency); ok && !k.Latency.GetUnmatched() && k.Latency.GetRttUs() > 0 {
		s := latSample{atUs: v.NowUs, rttMs: float64(k.Latency.GetRttUs()) / 1000}
		if k.Latency.GetKind() == pb.LatencySample_KIND_PONG {
			d.pongs.Push(s)
		} else {
			d.keepAlives.Push(s)
		}
	}
	pairUs := int64(c.Param(idLatencyPingspoof, "pair_window_ms", 2000) * 1000)
	for d.keepAlives.Len() > 0 {
		ka := d.keepAlives.At(0)
		if v.NowUs-ka.atUs < pairUs {
			break
		}
		d.popKeepAlive()
		d.pairKeepAlive(c, ka, pairUs)
	}
}

func (d *latencyDetector) popKeepAlive() {
	rest := d.keepAlives.Slice()[1:]
	d.keepAlives.Reset()
	for _, s := range rest {
		d.keepAlives.Push(s)
	}
}

func (d *latencyDetector) pairKeepAlive(c *Ctx, ka latSample, pairUs int64) {
	var near []float64
	for i := 0; i < d.pongs.Len(); i++ {
		p := d.pongs.At(i)
		if p.atUs >= ka.atUs-pairUs && p.atUs <= ka.atUs+pairUs {
			near = append(near, p.rttMs)
		}
	}
	if len(near) == 0 {
		return // unpaired: nothing to compare with
	}
	pong := Median(near)
	excess := ka.rttMs > pong+c.Param(idLatencyPingspoof, "excess_ms", 150) && ka.rttMs > pong*c.Param(idLatencyPingspoof, "ratio", 1.6)
	d.paired.push(excess)
	of := int(c.Param(idLatencyPingspoof, "of", 4))
	n := d.paired.count(of)
	if !excess || d.paired.size() < of || float64(n) < c.Param(idLatencyPingspoof, "need", 3) || !c.View.Judgeable() {
		return
	}
	cooldown := int64(c.Param(idLatencyPingspoof, "cooldown_s", 60) * 1e6)
	if d.lastPingUs != 0 && ka.atUs-d.lastPingUs < cooldown {
		return
	}
	d.lastPingUs = ka.atUs
	c.Emit(Emission{Check: idLatencyPingspoof, Points: 3, Confidence: 0.8,
		Info: fmt.Sprintf("keep-alive RTT %.0f ms vs %.0f ms ping/pong (%d of the last %d paired)", ka.rttMs, pong, n, of),
		Data: map[string]float64{"keepalive_ms": ka.rttMs, "pong_ms": pong, "excess": float64(n), "of": float64(of)}})
}

// ---------------------------------------------------------------------------------------------
// latency.blink
// ---------------------------------------------------------------------------------------------

// clientOriginated reports whether ev is caused by a client packet (server-side events such as velocity,
// teleports, env samples and stalls do not end a gap).
func clientOriginated(ev *pb.Event) bool {
	switch ev.GetKind().(type) {
	case *pb.Event_Move, *pb.Event_Attack, *pb.Event_Swing, *pb.Event_Place, *pb.Event_Dig, *pb.Event_Click,
		*pb.Event_UseItem, *pb.Event_Latency:
		return true
	}
	return false
}

func (b *blinkTracker) handle(c *Ctx, ev *pb.Event) {
	v := c.View
	if !v.TickClient {
		return
	}
	burstUs := int64(c.Param(idLatencyBlink, "burst_ms", 100) * 1000)
	if b.collecting && v.NowUs-b.burstUs > burstUs {
		b.closeBurst(c)
	}
	if b.confirming && v.NowUs >= b.confirmAt {
		b.confirm(c)
	}
	if !clientOriginated(ev) {
		return
	}
	_, attack := ev.GetKind().(*pb.Event_Attack)
	tick := ev.GetTick()
	gapUs := int64(c.Param(idLatencyBlink, "gap_ms", 500) * 1000)
	if !b.collecting && !b.confirming && b.lastUs != 0 && v.NowUs-b.lastUs > gapUs && tick != 0 && b.lastTick != 0 {
		attackWindow := int64(c.Param(idLatencyBlink, "attack_window_ms", 3000) * 1000)
		b.collecting = true
		b.gapStartUs, b.burstUs = b.lastUs, v.NowUs
		b.baseTick, b.minTick, b.maxTick = b.lastTick, tick, tick
		b.attacked = b.attackUs != 0 && b.gapStartUs-b.attackUs <= attackWindow
	}
	if b.collecting && tick != 0 {
		b.minTick, b.maxTick = min(b.minTick, tick), max(b.maxTick, tick)
		b.attacked = b.attacked || attack
	}
	if attack {
		b.attackUs = v.NowUs
	}
	b.lastUs = v.NowUs
	if tick != 0 {
		b.lastTick = tick
	}
}

func (b *blinkTracker) closeBurst(c *Ctx) {
	b.collecting = false
	from := max(b.baseTick, b.minTick-1)
	if b.maxTick <= from {
		return
	}
	b.ticks = b.maxTick - from
	if float64(b.ticks) < c.Param(idLatencyBlink, "burst_ticks", 10) || !b.attacked {
		return
	}
	b.confirming = true
	b.confirmAt = b.burstUs + int64(c.Param(idLatencyBlink, "stall_grace_ms", 1000)*1000)
}

// confirm counts the blink unless a server stall overlapped the gap or the player was teleported around
// it (world changes and respawns load terrain).
func (b *blinkTracker) confirm(c *Ctx) {
	b.confirming = false
	v := c.View
	if !v.Judgeable() || v.InStall(b.gapStartUs, b.burstUs) {
		return
	}
	if v.LastTeleportUs != 0 && v.LastTeleportUs >= b.gapStartUs-time1s && v.LastTeleportUs <= b.burstUs+time1s {
		return
	}
	b.marks.add(b.burstUs)
	windowUs := int64(c.Param(idLatencyBlink, "window_s", 60) * 1e6)
	n := b.marks.within(b.burstUs, windowUs)
	if float64(n) < c.Param(idLatencyBlink, "need", 3) {
		return
	}
	b.marks.reset()
	gapMs := float64(b.burstUs-b.gapStartUs) / 1000
	c.Emit(Emission{Check: idLatencyBlink, Points: 3, Confidence: 0.6,
		Info: fmt.Sprintf("%d blinks within 60 s (last: %.0f ms gap, then %d ticks in 100 ms)", n, gapMs, b.ticks),
		Data: map[string]float64{"blinks": float64(n), "gap_ms": gapMs, "ticks": float64(b.ticks)}})
}
