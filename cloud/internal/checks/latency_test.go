package checks_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/checks/checktest"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// latencyTimeline runs seconds of play: a movement packet every tick, a pong every second (another plugin
// pings) and a keep-alive every 15 s. kaRTT and pongRTT give the RTTs (ms) by sample time (s).
func latencyTimeline(r *rig, seconds int, kaRTT, pongRTT func(at float64) float64) {
	for tick := range seconds * 20 {
		at := float64(tick) / 20
		if tick%20 == 10 {
			r.latency(pb.LatencySample_KIND_PONG, pongRTT(at), false)
		}
		if tick%300 == 299 {
			r.latency(pb.LatencySample_KIND_KEEPALIVE, kaRTT(at), false)
		}
		r.step(geom.V(0.1, 0, 0))
	}
}

func constRTT(ms float64) func(float64) float64 { return func(float64) float64 { return ms } }

func TestPingspoofLegit(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 4))
	jitter := func(base float64) func(float64) float64 {
		return func(float64) float64 { return base + rng.Float64()*30 }
	}
	r := newRig(t, rigProtocol)
	latencyTimeline(r, 600, jitter(80), jitter(80))
	if es := r.run("latency"); len(es) != 0 {
		t.Fatalf("%+v", es)
	}
}

func TestPingspoofLegitWindowDragDelay(t *testing.T) {
	// Dragging the game window freezes the client: it holds the keep-alive reply until the window is
	// released (and the pongs of that moment are late too). One delayed keep-alive per minute never flags.
	r := newRig(t, rigProtocol)
	ka := func(at float64) float64 {
		if int(at)%60 == 44 { // the keep-alive at 45 s of every minute
			return 2600
		}
		return 60
	}
	pong := func(at float64) float64 {
		if int(at)%60 == 44 {
			return 1800
		}
		return 55
	}
	latencyTimeline(r, 600, ka, pong)
	if es := r.run("latency"); len(es) != 0 {
		t.Fatalf("%+v", es)
	}
}

func TestPingspoofCheatDelayedKeepAlives(t *testing.T) {
	r := newRig(t, rigProtocol)
	latencyTimeline(r, 305, constRTT(450), constRTT(50)) // keep-alive replies held 400 ms
	es := r.run("latency")
	expectCount(t, es, "latency.pingspoof", 5) // at most once per 60 s: 60, 120, 180, 240, 300 s
	expectCount(t, es, "latency.blink", 0)
	expectScoring(t, es, "latency.pingspoof", 3, 3, 0.8)
}

// netPath models the round trips the plugin observes (CHECKS.md 2.14): the server sends a keep-alive every
// kaEvery seconds (vanilla: 15 s; Paper and Folia 1.21.11: 1 s) and another plugin a ping every pingEvery
// seconds. Each direction is one ordered stream (TCP: a late packet holds back the ones after it). The
// vanilla client answers a keep-alive on its network thread as soon as it arrives, unless the main thread
// has been frozen in glfwPollEvents for 200 ms (window drag): then at the first client tick after the
// freeze. It answers a ping on the main thread, at the next frame.
type netPath struct {
	oneWayS   float64
	loss, rto float64      // per packet and direction; a lost packet is retransmitted after rto seconds
	lossy     [][2]float64 // spans with burstLoss instead of loss (Wi-Fi interference)
	burstLoss float64
	stalls    [][2]float64 // delivery stopped (proxy event loop, network): the backlog arrives at the end
	freezes   [][2]float64 // the client's main thread is frozen in pollEvents
	kaHold    func(sendS float64) float64
	seed      uint64
}

// inSpan returns the span of spans that contains at.
func inSpan(spans [][2]float64, at float64) (span [2]float64, ok bool) {
	for _, s := range spans {
		if at >= s[0] && at < s[1] {
			return s, true
		}
	}
	return span, false
}

// netPlay plays `seconds` of walking over path and reports every reply when it reaches the plugin, with
// RTT = arrival - send, as the plugin does.
func netPlay(r *rig, seconds, kaEvery, pingEvery float64, path netPath) {
	rng := rand.New(rand.NewPCG(path.seed, 7))
	type trip struct {
		kind               pb.LatencySample_Kind
		sent, answer, back float64
	}
	var trips []*trip
	for at := 0.3; at < seconds; at += kaEvery {
		trips = append(trips, &trip{kind: pb.LatencySample_KIND_KEEPALIVE, sent: at})
	}
	for at := 0.01; at < seconds; at += pingEvery {
		trips = append(trips, &trip{kind: pb.LatencySample_KIND_PONG, sent: at})
	}
	sort.SliceStable(trips, func(i, j int) bool { return trips[i].sent < trips[j].sent })
	lost := func(at float64) float64 {
		p := path.loss
		if _, ok := inSpan(path.lossy, at); ok {
			p = path.burstLoss
		}
		if rng.Float64() < p {
			return path.rto
		}
		return 0
	}
	var down, mainThread float64
	for _, tr := range trips {
		at := tr.sent + path.oneWayS + lost(tr.sent)
		if s, ok := inSpan(path.stalls, at); ok {
			at = s[1]
		}
		down = max(down, at)
		freeze, frozen := inSpan(path.freezes, down)
		if tr.kind == pb.LatencySample_KIND_PONG {
			answer := down
			if frozen {
				answer = freeze[1]
			}
			mainThread = max(mainThread, answer+0.008)
			tr.answer = mainThread
			continue
		}
		tr.answer = down
		if frozen && down-freeze[0] > 0.2 {
			tr.answer = freeze[1] + 0.05
		}
		if path.kaHold != nil {
			tr.answer += path.kaHold(tr.sent)
		}
	}
	sort.SliceStable(trips, func(i, j int) bool { return trips[i].answer < trips[j].answer })
	var up float64
	for _, tr := range trips {
		up = max(up, tr.answer+path.oneWayS+lost(tr.answer))
		tr.back = up
	}
	sort.SliceStable(trips, func(i, j int) bool { return trips[i].back < trips[j].back })
	start := r.p.TimeUs
	next := 0
	for tick := 0; float64(tick)*0.05 < seconds+5; tick++ {
		r.step(geom.V(0.1, 0, 0))
		for ; next < len(trips) && trips[next].back < float64(tick+1)*0.05; next++ {
			tr := trips[next]
			r.add(&pb.Event{Player: r.p.UUID, TimeUs: start + uint64(tr.back*1e6), Tick: r.p.Tick,
				Kind: &pb.Event_Latency{Latency: &pb.LatencySample{Kind: tr.kind, RttUs: uint32((tr.back - tr.sent) * 1e6)}}})
		}
	}
}

// everyMinute returns one span of d seconds per minute, starting at second 30.
func everyMinute(seconds, d float64) [][2]float64 {
	var out [][2]float64
	for s := 30.0; s < seconds; s += 60 {
		out = append(out, [2]float64{s, s + d})
	}
	return out
}

// saturatedStalls returns delivery stalls of 3-6 s, 3-12 s apart: a proxy or network path on a host whose
// CPU is saturated.
func saturatedStalls(seed uint64, seconds float64) [][2]float64 {
	rng := rand.New(rand.NewPCG(seed, 11))
	var out [][2]float64
	for s := 10 + rng.Float64()*3; s < seconds; {
		d := 3 + rng.Float64()*3
		out = append(out, [2]float64{s, s + d})
		s += d + 3 + rng.Float64()*9
	}
	return out
}

func pingspoofCount(t *testing.T, seconds, kaEvery, pingEvery float64, path netPath) int {
	t.Helper()
	r := newRig(t, rigProtocol)
	netPlay(r, seconds, kaEvery, pingEvery, path)
	es := r.run("latency")
	expectCount(t, es, "latency.blink", 0)
	return checktest.Count(es, "latency.pingspoof")
}

const (
	paperKeepAlive   = 1.0  // Paper and Folia 1.21.11 send a keep-alive every second
	vanillaKeepAlive = 15.0 // vanilla and Spigot every 15 s
)

func TestPingspoofLegitBacklog(t *testing.T) {
	// When delivery stops after the server sent its packets (a stalled proxy event loop, a network outage,
	// a frozen client), the replies of seconds of keep-alives and pings arrive at once. The keep-alives and
	// pings sent at the same moment are equally late, so nothing is in excess.
	cases := []struct {
		name               string
		kaEvery, pingEvery float64
		path               netPath
	}{
		{"paper cadence, 5 s delivery stall each minute", paperKeepAlive, 0.05, netPath{oneWayS: 0.03, stalls: everyMinute(300, 5)}},
		{"paper cadence, 5 s window drag each minute", paperKeepAlive, 0.05, netPath{oneWayS: 0.03, freezes: everyMinute(300, 5)}},
		{"paper cadence, 5 s window drag, a ping per second", paperKeepAlive, 1, netPath{oneWayS: 0.03, freezes: everyMinute(300, 5)}},
		{"paper cadence, 8 s delivery stall, 200 pings per second", paperKeepAlive, 0.005, netPath{oneWayS: 0.03, stalls: everyMinute(300, 8)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if n := pingspoofCount(t, 300, c.kaEvery, c.pingEvery, c.path); n != 0 {
				t.Fatalf("%d emissions", n)
			}
		})
	}
	for _, kaEvery := range []float64{paperKeepAlive, vanillaKeepAlive} {
		for _, pingEvery := range []float64{0.05, 1} {
			t.Run(fmt.Sprintf("saturated proxy, keep-alive every %.0f s, ping every %.2f s", kaEvery, pingEvery), func(t *testing.T) {
				total := 0
				for seed := range uint64(6) {
					total += pingspoofCount(t, 600, kaEvery, pingEvery, netPath{oneWayS: 0.03, stalls: saturatedStalls(seed, 600)})
				}
				if total != 0 {
					t.Fatalf("%d emissions in an hour behind a stalling proxy", total)
				}
			})
		}
	}
}

func TestPingspoofCheatHeldKeepAlives(t *testing.T) {
	hold400 := func(float64) float64 { return 0.4 }
	// Released in batches: every keep-alive is held until the next full 2 s.
	batches := func(sent float64) float64 { return 2 - math.Mod(sent+0.03, 2) }
	cases := []struct {
		name               string
		kaEvery, pingEvery float64
		path               netPath
		want               int
	}{
		{"paper cadence, held 400 ms", paperKeepAlive, 0.05, netPath{oneWayS: 0.03, kaHold: hold400}, 5},
		{"paper cadence, held 400 ms, a ping per second", paperKeepAlive, 1, netPath{oneWayS: 0.03, kaHold: hold400}, 5},
		{"paper cadence, held 400 ms, 200 pings per second", paperKeepAlive, 0.005, netPath{oneWayS: 0.03, kaHold: hold400}, 5},
		{"paper cadence, released in batches", paperKeepAlive, 0.05, netPath{oneWayS: 0.03, kaHold: batches}, 5},
		{"vanilla cadence, held 400 ms", vanillaKeepAlive, 0.05, netPath{oneWayS: 0.03, kaHold: hold400}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if n := pingspoofCount(t, 300, c.kaEvery, c.pingEvery, c.path); n != c.want {
				t.Fatalf("%d emissions, want %d", n, c.want)
			}
		})
	}
	t.Run("held 400 ms behind a stalling proxy", func(t *testing.T) {
		// Stalls do not hide a client that holds its keep-alive replies.
		for seed := range uint64(3) {
			path := netPath{oneWayS: 0.03, stalls: saturatedStalls(seed, 600), kaHold: hold400}
			if n := pingspoofCount(t, 600, paperKeepAlive, 0.05, path); n < 5 {
				t.Fatalf("seed %d: %d emissions in 10 min, want >= 5", seed, n)
			}
		}
	})
}

// everyTwoMinutes returns one span of d seconds every 2 minutes, starting at second 20.
func everyTwoMinutes(seconds, d float64) [][2]float64 {
	var out [][2]float64
	for s := 20.0; s < seconds; s += 120 {
		out = append(out, [2]float64{s, s + d})
	}
	return out
}

func TestPingspoofLegitLossyLink(t *testing.T) {
	// A lost packet is retransmitted after 200+ ms and holds back the packets behind it: the keep-alive
	// replies it hits are in excess of the pongs sent around them. At Paper's cadence "3 of the last 4"
	// judged 3 s of play, so three hits in a row were a violation (5% loss: 5-26 per hour).
	cases := []struct {
		name      string
		pingEvery float64
		path      netPath
	}{
		{"5% loss, 20 pings per second", 0.05, netPath{oneWayS: 0.03, loss: 0.05, rto: 0.25}},
		{"5% loss, a ping per second", 1, netPath{oneWayS: 0.03, loss: 0.05, rto: 0.25}},
		{"10% loss, a ping per second", 1, netPath{oneWayS: 0.03, loss: 0.1, rto: 0.25}},
		{"30 s of 30% loss every 2 minutes", 1, netPath{oneWayS: 0.03, loss: 0.01, rto: 0.25, lossy: everyTwoMinutes(3600, 30), burstLoss: 0.3}},
	}
	seconds := 3600.0
	if testing.Short() {
		seconds = 1200
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for seed := range uint64(2) {
				c.path.seed = seed
				if n := pingspoofCount(t, seconds, paperKeepAlive, c.pingEvery, c.path); n != 0 {
					t.Fatalf("seed %d: %d emissions in %.0f min", seed, n, seconds/60)
				}
			}
		})
	}
}

func TestPingspoofCheatHeldOnBadLinks(t *testing.T) {
	hold400 := func(float64) float64 { return 0.4 }
	bursts := func(d float64) func(float64) float64 {
		spans := everyTwoMinutes(600, d)
		return func(sent float64) float64 {
			if _, ok := inSpan(spans, sent); ok {
				return 0.4
			}
			return 0
		}
	}
	cases := []struct {
		name      string
		pingEvery float64
		path      netPath
		want      int
	}{
		{"held 400 ms on a 5% lossy link", 0.05, netPath{oneWayS: 0.03, loss: 0.05, rto: 0.25, kaHold: hold400}, 10},
		{"held 400 ms on a 5% lossy link, a ping per second", 1, netPath{oneWayS: 0.03, loss: 0.05, rto: 0.25, kaHold: hold400}, 10},
		{"held 200 ms", 0.05, netPath{oneWayS: 0.03, kaHold: func(float64) float64 { return 0.2 }}, 10},
		{"held 400 ms for 20 s every 2 minutes", 0.05, netPath{oneWayS: 0.03, kaHold: bursts(20)}, 5},
		{"held 400 ms for 20 s every 2 minutes, a ping per second", 1, netPath{oneWayS: 0.03, kaHold: bursts(20)}, 5},
		{"held 400 ms for 30 s every 2 minutes on a 5% lossy link", 0.05, netPath{oneWayS: 0.03, loss: 0.05, rto: 0.25, kaHold: bursts(30)}, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if n := pingspoofCount(t, 600, paperKeepAlive, c.pingEvery, c.path); n != c.want {
				t.Fatalf("%d emissions in 10 min, want %d", n, c.want)
			}
		})
	}
}

func TestPingspoofNotJudged(t *testing.T) {
	t.Run("unmatched", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		for tick := range 305 * 20 {
			if tick%20 == 10 {
				r.latency(pb.LatencySample_KIND_PONG, 50, false)
			}
			if tick%300 == 299 {
				r.latency(pb.LatencySample_KIND_KEEPALIVE, 900, true) // a reply we never saw sent
			}
			r.step(geom.V(0.1, 0, 0))
		}
		if es := r.run("latency"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("no pongs", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		for tick := range 305 * 20 {
			if tick%300 == 299 {
				r.latency(pb.LatencySample_KIND_KEEPALIVE, 900, false)
			}
			r.step(geom.V(0.1, 0, 0))
		}
		if es := r.run("latency"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("only three keep-alives", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		latencyTimeline(r, 50, constRTT(450), constRTT(50))
		if es := r.run("latency"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("keep-alives held for less than 20 s", func(t *testing.T) {
		// The evidence must span 20 s of keep-alives (at Paper's cadence 16 of the last 21): a 10 s hold is
		// too short, as a single held keep-alive is at the vanilla cadence.
		spans := everyTwoMinutes(600, 10)
		hold := func(sent float64) float64 {
			if _, ok := inSpan(spans, sent); ok {
				return 0.4
			}
			return 0
		}
		if n := pingspoofCount(t, 600, paperKeepAlive, 0.05, netPath{oneWayS: 0.03, kaHold: hold}); n != 0 {
			t.Fatalf("%d emissions", n)
		}
	})
}

// burst releases n held movement packets (one per client tick) within a few milliseconds.
func burst(r *rig, n int) {
	for range n {
		e := r.at()
		e.Kind = &pb.Event_Move{Move: &pb.MoveSample{X: r.p.Pos.X, Y: r.p.Pos.Y, Z: r.p.Pos.Z, HasPos: true, HasRot: true,
			OnGround: true, TicksSinceLast: 1, PacketsSinceLast: 1, Focus: true}}
		r.add(e)
		r.p.Tick++
		r.p.TimeUs += 300
	}
}

// fight sends ticks of combat: a movement packet every tick, an attack and swing every 10 ticks.
func fight(r *rig, ticks int) {
	for i := range ticks {
		if i%10 == 0 {
			r.attack()
			r.swing(pb.Hand_HAND_MAIN)
		}
		r.step(geom.V(0.1, 0, 0.1))
	}
}

func TestBlinkCheat(t *testing.T) {
	r := newRig(t, rigProtocol)
	for range 6 {
		fight(r, 30)
		r.wait(800 * time.Millisecond) // packets held
		burst(r, 16)
		fight(r, 100)
	}
	es := r.run("latency")
	expectCount(t, es, "latency.blink", 2) // 6 blinks within 60 s: violations at the 3rd and 6th
	expectScoring(t, es, "latency.blink", 3, 3, 0.6)
}

func TestBlinkLegit(t *testing.T) {
	cases := map[string]func(r *rig){
		"server stall": func(r *rig) {
			fight(r, 30)
			start := r.p.TimeUs
			r.wait(800 * time.Millisecond)
			burst(r, 16)
			r.stall(start+50_000, start+750_000) // reported after the stall ended
			fight(r, 100)
		},
		"still after combat": func(r *rig) {
			// A tick client that stands still sends one movement packet per second.
			fight(r, 30)
			for range 5 {
				for range 19 {
					r.idle()
				}
				r.step(geom.V(0, 0, 0))
			}
			fight(r, 40)
		},
		"lag without combat": func(r *rig) {
			for range 100 {
				r.step(geom.V(0.1, 0, 0))
			}
			r.wait(800 * time.Millisecond)
			burst(r, 16)
			for range 100 {
				r.step(geom.V(0.1, 0, 0))
			}
		},
		"short hiccup": func(r *rig) {
			// 6 held ticks plus the 2 regular ones of the next 100 ms stay below 10.
			fight(r, 30)
			r.wait(700 * time.Millisecond)
			burst(r, 6)
			fight(r, 100)
		},
		"teleport": func(r *rig) {
			fight(r, 30)
			r.teleport()
			r.wait(800 * time.Millisecond)
			burst(r, 16)
			fight(r, 100)
		},
	}
	for name, play := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigProtocol)
			for range 6 {
				play(r)
			}
			if es := r.run("latency"); len(es) != 0 {
				t.Fatalf("%+v", es)
			}
		})
	}
	t.Run("legacy client", func(t *testing.T) {
		r := newRig(t, 340)
		for range 6 {
			fight(r, 30)
			r.wait(800 * time.Millisecond)
			burst(r, 16)
			fight(r, 100)
		}
		if es := r.run("latency"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
	t.Run("two blinks a minute", func(t *testing.T) {
		r := newRig(t, rigProtocol)
		for range 4 {
			fight(r, 30)
			r.wait(800 * time.Millisecond)
			burst(r, 16)
			fight(r, 600) // 31.5 s per cycle
		}
		if es := r.run("latency"); len(es) != 0 {
			t.Fatalf("%+v", es)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// Property test: random legit sessions never emit (all six families).
// ---------------------------------------------------------------------------------------------

// legitSession plays about `seconds` of mixed vanilla-consistent play: fights with human clicking and
// knockback, bridging and building while turning, mining, inventory use while standing, idling, rare lag
// spikes, and latency samples with jitter.
func legitSession(r *rig, rng *rand.Rand, seconds int) {
	end := r.p.TimeUs + uint64(seconds)*1_000_000
	start := r.p.TimeUs
	nextPong, nextKA := start, start+15_000_000
	lastSpike := start
	baseRTT := 30 + rng.Float64()*120
	samples := func() {
		for r.p.TimeUs >= nextPong {
			r.latency(pb.LatencySample_KIND_PONG, baseRTT+rng.Float64()*25, false)
			nextPong += 1_000_000
		}
		for r.p.TimeUs >= nextKA {
			r.latency(pb.LatencySample_KIND_KEEPALIVE, baseRTT+rng.Float64()*25, false)
			nextKA += 15_000_000
		}
	}
	r.tick(geom.V(0.5, 64, 0.5), geom.Rotation{})
	h := 0.0
	for r.p.TimeUs < end {
		switch seg := rng.IntN(10); {
		case seg < 4: // fight
			nextClick := 0.0
			mean := 70 + rng.Float64()*90
			for range 40 + rng.IntN(120) {
				for nextClick < 50 {
					if rng.IntN(3) == 0 {
						r.attack()
					}
					r.swing(pb.Hand_HAND_MAIN)
					nextClick += math.Max(20, mean+rng.NormFloat64()*25)
				}
				nextClick -= 50
				if rng.IntN(25) == 0 { // knockback, applied two ticks later
					v := geom.V(rng.Float64()*0.6-0.3, 0.36+rng.Float64()*0.06, rng.Float64()*0.6-0.3)
					r.velocity(v)
					r.step(geom.V(0, 0, 0))
					samples()
					r.step(geom.V(0, 0, 0))
					r.ground = false
					mot := v
					for range 12 {
						d := mot
						if r.p.Pos.Y+d.Y <= 64 {
							d.Y, r.ground = 64-r.p.Pos.Y, true
						}
						r.step(d)
						samples()
						mot = geom.V(mot.X*0.91, (mot.Y-0.08)*0.98, mot.Z*0.91)
						if r.ground {
							break
						}
					}
					r.ground = true
					r.tick(geom.V(r.p.Pos.X, 64, r.p.Pos.Z), r.p.Rot)
					h = 0
				}
				rot := geom.Rotation{Yaw: r.p.Rot.Yaw + rng.NormFloat64()*8, Pitch: math.Max(-60, math.Min(60, r.p.Rot.Pitch+rng.NormFloat64()*3))}
				h = math.Min(0.28, h*0.546+0.13*rng.Float64())
				r.tick(r.p.Pos.Add(geom.LookVector(rot.Yaw, 0).Scale(h)), rot)
				samples()
			}
		case seg < 5: // building
			randomPlacements(r, rng, 3+rng.IntN(10))
			samples()
			h = 0
		case seg < 6: // mining
			for range 1 + rng.IntN(4) {
				eye := r.eyeOf(false)
				rot := geom.Rotation{Yaw: rng.Float64()*360 - 180, Pitch: rng.Float64()*120 - 30}
				p := eye.Add(rot.Look().Scale(1 + rng.Float64()*3))
				b := [3]int32{int32(math.Floor(p.X)), int32(math.Floor(p.Y)), int32(math.Floor(p.Z))}
				face, _, ok := rayClick(eye, rot, b)
				if !ok {
					continue
				}
				c := blockClick{block: b, face: face, eye: eye, rot: rot}
				r.tick(r.p.Pos, rot)
				r.dig(pb.BlockDig_ACTION_START, c)
				r.swing(pb.Hand_HAND_MAIN)
				r.tick(r.p.Pos, rot)
				for range rng.IntN(40) {
					r.swing(pb.Hand_HAND_MAIN)
					r.tick(r.p.Pos, rot)
					samples()
				}
				r.dig(pb.BlockDig_ACTION_FINISH, c)
				r.tick(r.p.Pos, rot)
			}
			h = 0
		case seg < 7: // inventory: clicking while sliding to a halt, then closing and walking off
			for range 10 + rng.IntN(60) {
				if rng.IntN(3) > 0 {
					r.click(0, false)
				}
				h *= 0.546
				r.tick(r.p.Pos.Add(geom.LookVector(r.p.Rot.Yaw, 0).Scale(h)), r.p.Rot)
				samples()
			}
			r.click(0, false) // the last click, then E in the same tick: the player turns and walks off
			r.tick(r.p.Pos.Add(geom.V(0.13, 0, 0)), geom.Rotation{Yaw: r.p.Rot.Yaw + 20, Pitch: r.p.Rot.Pitch})
			r.step(geom.V(0.2, 0, 0))
			h = 0.2
		case seg < 8: // idle: one movement packet per second
			for range 1 + rng.IntN(5) {
				for range 19 {
					r.idle()
				}
				r.step(geom.V(0, 0, 0))
				samples()
			}
			h = 0
		default: // walking, with a rare lag spike
			for range 20 + rng.IntN(60) {
				r.step(geom.V(0.2, 0, 0))
				samples()
			}
			if r.p.TimeUs-lastSpike > 70_000_000 && rng.IntN(2) == 0 {
				r.wait(time.Duration(600+rng.IntN(400)) * time.Millisecond)
				burst(r, 12+rng.IntN(8))
				lastSpike = r.p.TimeUs
			}
			h = 0.2
		}
		samples()
	}
	r.step(geom.V(0, 0, 0))
}

func TestPropertyRandomLegitSessions(t *testing.T) {
	families := []string{"velocity", "autoclicker", "scaffold", "dig", "inventory", "latency"}
	seeds := uint64(12)
	if testing.Short() {
		seeds = 3
	}
	total := 0
	for seed := range seeds {
		r := newRig(t, rigProtocol)
		legitSession(r, rand.New(rand.NewPCG(seed, 2026)), 300)
		for _, f := range families {
			es := checktest.Run(t, rigFactory(t, f), r.events)
			for _, e := range es {
				t.Errorf("seed %d %s: %s %s", seed, f, e.Check, e.Info)
			}
			total += len(es)
		}
	}
	if total != 0 {
		t.Fatalf("%d emissions over %d legit 5-minute sessions", total, seeds)
	}
}
