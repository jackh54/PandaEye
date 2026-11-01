package worker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/config"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	dto "github.com/prometheus/client_model/go"
)

// testClock is a settable worker clock.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

var digestT0 = time.Date(2026, 11, 1, 13, 20, 0, 0, time.UTC)

func newTestDigest(t *testing.T, clock *testClock) *digestWriter {
	t.Helper()
	w, err := openDigest(filepath.Join(t.TempDir(), "digests"), 30*24*time.Hour, nil, metrics.New(), clock.now)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// readDigest parses every line of the hour's file.
func readDigest(t *testing.T, w *digestWriter, hour time.Time) []digestLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.dir, digestName(hour.Unix())))
	if err != nil {
		t.Fatal(err)
	}
	var out []digestLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var l digestLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

func lineOf(t *testing.T, lines []digestLine, check string) digestLine {
	t.Helper()
	var found []digestLine
	for _, l := range lines {
		if l.Check == check {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: %d lines in %+v", check, len(found), lines)
	}
	return found[0]
}

func drops(t *testing.T, m *metrics.Metrics, reason string) float64 {
	t.Helper()
	var d dto.Metric
	if err := m.Drops.WithLabelValues(reason).Write(&d); err != nil {
		t.Fatal(err)
	}
	return d.GetCounter().GetValue()
}

func u64(p *uint64) uint64 {
	if p == nil {
		return math.MaxUint64
	}
	return *p
}

// place is a BlockPlace against the north face of b from the player's standing eye.
func place(p *testkit.Player, b [3]int32, rot geom.Rotation) *pb.Event {
	return placeAt(p, b, pb.Face_FACE_NORTH, geom.V(0.5, 0.5, 0), rot)
}

// placeAt is a BlockPlace against the face of b at cursor from the player's standing eye.
func placeAt(p *testkit.Player, b [3]int32, face pb.Face, cursor geom.Vec3, rot geom.Rotation) *pb.Event {
	eye := p.Eye()
	return &pb.Event{Player: p.UUID, TimeUs: p.TimeUs, Tick: p.Tick, Kind: &pb.Event_Place{Place: &pb.BlockPlace{
		Block: &pb.Vec3I{X: b[0], Y: b[1], Z: b[2]}, Face: face,
		CursorX: float32(cursor.X), CursorY: float32(cursor.Y), CursorZ: float32(cursor.Z),
		Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, Yaw: float32(rot.Yaw), Pitch: float32(rot.Pitch), BlockReach: 4.5,
		Item: "minecraft:stone"}}}
}

func dig(p *testkit.Player, action pb.BlockDig_Action, b [3]int32, rot geom.Rotation) *pb.Event {
	eye := p.Eye()
	return &pb.Event{Player: p.UUID, TimeUs: p.TimeUs, Tick: p.Tick, Kind: &pb.Event_Dig{Dig: &pb.BlockDig{Action: action,
		Block: &pb.Vec3I{X: b[0], Y: b[1], Z: b[2]}, Face: pb.Face_FACE_NORTH, Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z},
		Yaw: float32(rot.Yaw), Pitch: float32(rot.Pitch), BlockReach: 4.5}}}
}

// A scripted session: legit and failing placements and digs, a forwarded local flag and a cloud check
// emission are counted in their cells; a silent batch (warm start) counts nothing; the file holds no
// player data.
func TestDigestCountsEngineCells(t *testing.T) {
	clock := &testClock{t: digestT0}
	h := newHarness(t, nil)
	h.env.now = clock.now
	w := newTestDigest(t, clock)
	h.env.digest = w
	h.eng = newEngine(3, h.env, nil)
	srv := testkit.NewServerID()
	o := origin(srv, pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("BuilderBob")
	p.Pos = geom.V(0.5, 64, 0.5)
	brand := p.Join()
	brand.GetJoin().Update, brand.GetJoin().Brand = true, "lunarclient:v2.18.3-2451"
	var sigs []*pb.Signal
	send := func(silent bool, evs ...*pb.Event) {
		t.Helper()
		sigs = append(sigs, signals(h.send(o, h.seq+1, silent, evs...))...)
	}
	block := [3]int32{0, 64, 2}
	aimed, _ := geom.Angles(geom.V(0.5, 64.5, 2).Sub(p.Eye())) // the centre of the north face
	away := geom.Rotation{Yaw: aimed.Yaw + 180, Pitch: aimed.Pitch}
	send(false, p.Join(), p.State(), brand, p.Move(geom.Vec3{}, aimed))
	builds := func(rot geom.Rotation, n int) (evs []*pb.Event) {
		for range n {
			evs = append(evs, p.Move(geom.Vec3{}, rot), place(p, block, rot), p.Move(geom.Vec3{}, rot))
		}
		return evs
	}
	send(false, builds(aimed, 5)...)
	send(false, builds(away, 6)...)
	send(true, builds(away, 6)...) // a warm start replays it silently
	var digs []*pb.Event
	for _, rot := range []geom.Rotation{aimed, aimed, away, away, away, away} {
		digs = append(digs, p.Move(geom.Vec3{}, rot), dig(p, pb.BlockDig_ACTION_START, block, rot), p.Move(geom.Vec3{}, rot),
			dig(p, pb.BlockDig_ACTION_FINISH, block, rot), p.Move(geom.Vec3{}, rot))
	}
	send(false, digs...)
	p.Advance(10 * time.Millisecond)
	send(false, p.Flag("movement.speed", pb.Category_CATEGORY_MOVEMENT, 0.5))
	send(false, reachCheat(p, 4)...)

	h.eng.housekeep(clock.t)
	w.merge(<-w.in)
	if len(w.in) != 0 {
		t.Fatal("one flush per housekeeping")
	}
	clock.t = digestT0.Add(40*time.Minute + 59*time.Second) // 14:00:59: the 13:00 hour is due at 14:01
	w.writeDue(false)
	if len(w.pending) != 1 {
		t.Fatal("an hour is written only after its grace period")
	}
	clock.t = digestT0.Add(42 * time.Minute)
	w.writeDue(false)
	if len(w.pending) != 0 {
		t.Fatal("the hour is due")
	}
	hour := digestT0.Truncate(time.Hour)
	raw, err := os.ReadFile(filepath.Join(w.dir, "digest-20261101T13Z.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{p.Name, identity.UUID(p.UUID).String(), p.Session, "64,2", "Info", "info"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("the digest holds %q:\n%s", private, raw)
		}
	}
	lines := readDigest(t, w, hour)
	emitted := func(check string) uint64 {
		n := uint64(0)
		for _, s := range sigs {
			if s.CheckId == check {
				n++
			}
		}
		return n
	}
	angle := checks.DigestEdges("scaffold.ray")
	for _, c := range []struct {
		check                      string
		evaluations, failures, low uint64 // low: values in the first two buckets
	}{
		{"scaffold.ray", 11, 6, 5}, {"scaffold.cursor", 11, 6, 5}, {"scaffold.face", 11, 0, 0}, {"scaffold.reach", 11, 0, 0},
		{"dig.ray", 12, 8, 4},
	} {
		l := lineOf(t, lines, c.check)
		if l.Hour != "2026-11-01T13:00:00Z" || l.ServerID != srv || l.Protocol == nil || *l.Protocol != 769 || l.Brand != "lunar" ||
			l.TickClient == nil || !*l.TickClient || l.Pose != "standing" || l.Ping != "<50" || l.Overflow {
			t.Errorf("%s: dimensions %+v", c.check, l)
		}
		if u64(l.Evaluations) != c.evaluations || u64(l.Failures) != c.failures || l.Emissions != emitted(c.check) ||
			u64(l.EyePath) != c.evaluations || u64(l.Eyes) != 2*c.evaluations || u64(l.Rotations) != c.evaluations {
			t.Errorf("%s: counters %+v (emitted %d)", c.check, l, emitted(c.check))
		}
		sum := uint64(0)
		for _, n := range l.Hist {
			sum += n
		}
		if sum != c.evaluations || len(l.Hist) != len(l.LE)+1 || !slices.Equal(l.LE, checks.DigestEdges(c.check)) {
			t.Errorf("%s: histogram %v over %v", c.check, l.Hist, l.LE)
		}
		if c.low > 0 && (l.Hist[0]+l.Hist[1] != c.low || l.Hist[len(angle)] != c.failures) {
			t.Errorf("%s: hits/aligned in the first buckets, misses above 90 degrees: %v", c.check, l.Hist)
		}
	}
	if face := lineOf(t, lines, "scaffold.face"); face.Hist[slices.Index(face.LE, 3)] != 11 { // margin 2.53
		t.Errorf("face margins %v", face.Hist)
	}
	if reach := lineOf(t, lines, "scaffold.reach"); reach.Hist[slices.Index(reach.LE, -3)] != 11 { // excess -3.48
		t.Errorf("reach excess %v", reach.Hist)
	}
	if emitted("scaffold.ray") != 2 || emitted("dig.ray") != 2 || emitted("movement.speed") != 1 || emitted("reach.distance") == 0 {
		t.Fatalf("fixture: signals %v", sigs)
	}
	// Every other check that emitted has one emissions-only cell with its signal count.
	for _, s := range sigs {
		lineOf(t, lines, s.CheckId)
	}
	for _, l := range lines {
		if checks.DigestEdges(l.Check) != nil {
			continue
		}
		if l.Emissions == 0 || l.Emissions != emitted(l.Check) || l.ServerID != srv || l.Protocol == nil || *l.Protocol != 769 ||
			l.Brand != "lunar" || l.Evaluations != nil || l.TickClient != nil || l.Pose != "" || l.Ping != "" || l.Hist != nil {
			t.Errorf("%s: emissions-only cell %+v (emitted %d)", l.Check, l, emitted(l.Check))
		}
	}
}

// The world-interaction cells break their evaluations and failures down by the context of the actions: a
// scripted session of placements and digs (close and far, a burst, turning, moving, pauses, every face,
// START and FINISH, a cursor-only failure) counts each action in its bucket of every context.
func TestDigestContextCounts(t *testing.T) {
	clock := &testClock{t: digestT0}
	h := newHarness(t, nil)
	h.env.now = clock.now
	w := newTestDigest(t, clock)
	h.env.digest = w
	h.eng = newEngine(3, h.env, nil)
	p := testkit.NewPlayer("ContextCarl")
	p.Protocol = 763 // 1.20.1: R1 is the next move within 100 ms, whatever its tick
	p.Pos = geom.V(0.5, 64, 0.5)
	evs := []*pb.Event{p.Join(), p.State()}
	// An action and its expected buckets: dist, turn, gap, speed and face (placements) or action (digs).
	type act struct {
		ctx  [5]int
		fail []string // the checks it fails
	}
	var places, digs []act
	move := func(d geom.Vec3, rot geom.Rotation) { evs = append(evs, p.Move(d, rot)) }
	aimAt := func(target geom.Vec3) geom.Rotation { r, _ := geom.Angles(target.Sub(p.Eye())); return r }
	pitched := func(r geom.Rotation, d float64) geom.Rotation { return geom.Rotation{Yaw: r.Yaw, Pitch: r.Pitch + d} }
	north, middle := pb.Face_FACE_NORTH, geom.V(0.5, 0.5, 0)
	faceOf := func(b [3]int32) geom.Vec3 { return geom.V(float64(b[0])+0.5, float64(b[1])+0.5, float64(b[2])) } // north face centre
	click := func(b [3]int32, face pb.Face, cursor geom.Vec3, rot geom.Rotation, a act) {
		evs = append(evs, placeAt(p, b, face, cursor, rot))
		places = append(places, a)
	}
	mine := func(action pb.BlockDig_Action, b [3]int32, rot geom.Rotation, a act) {
		evs = append(evs, dig(p, action, b, rot))
		digs = append(digs, a)
	}
	both := []string{"scaffold.ray", "scaffold.cursor"}

	// Standing at 0.5, 64, 0.5: the first placement, close, before an eye path; 50 ms later two in the
	// same tick.
	close, mid := [3]int32{0, 65, 1}, [3]int32{0, 64, 2}
	rA, rB := aimAt(faceOf(close)), aimAt(faceOf(mid))
	move(geom.Vec3{}, rA)
	click(close, north, middle, rA, act{ctx: [5]int{0, 0, 3, 0, 2}})
	move(geom.Vec3{}, rA)
	click(mid, north, middle, rB, act{ctx: [5]int{1, 0, 0, 1, 2}})
	click(mid, north, middle, rB, act{ctx: [5]int{1, 0, 0, 1, 2}})
	move(geom.Vec3{}, rB)
	// Every 100 ms, the next move (R1) turning down by 3, 40 and 0 degrees.
	for _, c := range []struct {
		deg  float64
		turn int
	}{{3, 1}, {40, 3}, {0, 0}} {
		move(geom.Vec3{}, rB)
		click(mid, north, middle, rB, act{ctx: [5]int{1, c.turn, 1, 1, 2}})
		move(geom.Vec3{}, pitched(rB, c.deg))
	}
	// The cursor in a corner of the face the ray hits, turning by 15 degrees: a cursor-only failure; 50 ms
	// later looking away at a farther block: ray and cursor fail. (The engine's shard flushes in between:
	// the writer adds both parts up.)
	move(geom.Vec3{}, rB)
	click(mid, north, geom.V(0.05, 0.95, 0), rB, act{ctx: [5]int{1, 2, 1, 1, 2}, fail: []string{"scaffold.cursor"}})
	move(geom.Vec3{}, pitched(rB, 15))
	firstFlush := len(evs)
	away := geom.Rotation{Yaw: rB.Yaw + 180, Pitch: rB.Pitch}
	click([3]int32{0, 64, 4}, north, middle, away, act{ctx: [5]int{2, 0, 0, 1, 2}, fail: both})
	move(geom.Vec3{}, away)
	// Walking east by 0.1, 0.3 and 0.5 blocks a move, the first 250 ms after the last placement.
	move(geom.Vec3{}, away)
	move(geom.Vec3{}, away)
	move(geom.Vec3{}, away)
	for _, c := range []struct {
		step       float64
		gap, speed int
	}{{0.1, 2, 2}, {0.3, 1, 3}, {0.5, 1, 4}} {
		move(geom.V(c.step, 0, 0), away)
		r := aimAt(faceOf(mid))
		click(mid, north, middle, r, act{ctx: [5]int{1, 0, c.gap, c.speed, 2}})
		move(geom.Vec3{}, r)
	}
	// At 1.4, 64, 0.5 after a second's pause, a far block; 200 ms later, without a move since (no eye path),
	// one at the reach limit.
	p.Advance(time.Second)
	far, farther := [3]int32{1, 64, 4}, [3]int32{1, 64, 5}
	r := aimAt(faceOf(far))
	move(geom.Vec3{}, r)
	click(far, north, middle, r, act{ctx: [5]int{2, 0, 3, 1, 2}})
	move(geom.Vec3{}, r)
	p.Advance(200 * time.Millisecond)
	r = aimAt(faceOf(farther))
	click(farther, north, middle, r, act{ctx: [5]int{3, 0, 2, 0, 2}})
	move(geom.Vec3{}, r)
	// The other faces: the top of the floor in front, the bottom of a block overhead, a special face
	// (scaffold.face does not judge it).
	for _, c := range []struct {
		b            [3]int32
		face         pb.Face
		cursor       geom.Vec3
		dist, bucket int
	}{
		{[3]int32{1, 63, 1}, pb.Face_FACE_UP, geom.V(0.5, 1, 0.5), 1, 0},
		{[3]int32{1, 66, 1}, pb.Face_FACE_DOWN, geom.V(0.5, 0, 0.5), 0, 1},
		{[3]int32{1, 64, 2}, pb.Face_FACE_OTHER, middle, 1, 3},
	} {
		r := aimAt(geom.BlockBox(c.b[0], c.b[1], c.b[2]).Min.Add(c.cursor))
		move(geom.Vec3{}, r)
		click(c.b, c.face, c.cursor, r, act{ctx: [5]int{c.dist, 0, 1, 1, c.bucket}})
		move(geom.Vec3{}, r)
	}
	// Digs: START (the first) and FINISH 150 ms later in view, then START and FINISH looking away 50 ms
	// apart, the FINISH's R1 turning by 10 degrees.
	wall := [3]int32{1, 64, 2}
	r = aimAt(faceOf(wall))
	move(geom.Vec3{}, r)
	mine(pb.BlockDig_ACTION_START, wall, r, act{ctx: [5]int{1, 0, 3, 1, 0}})
	move(geom.Vec3{}, r)
	move(geom.Vec3{}, r)
	move(geom.Vec3{}, r)
	mine(pb.BlockDig_ACTION_FINISH, wall, r, act{ctx: [5]int{1, 0, 1, 1, 1}})
	move(geom.Vec3{}, r)
	away = geom.Rotation{Yaw: r.Yaw + 180, Pitch: r.Pitch}
	move(geom.Vec3{}, away)
	mine(pb.BlockDig_ACTION_START, wall, away, act{ctx: [5]int{1, 0, 1, 1, 0}, fail: []string{"dig.ray"}})
	move(geom.Vec3{}, away)
	mine(pb.BlockDig_ACTION_FINISH, wall, away, act{ctx: [5]int{1, 2, 0, 1, 1}, fail: []string{"dig.ray"}})
	move(geom.Vec3{}, pitched(away, 10))
	evs = append(evs, flag(p, "movement.fly", 1)) // an emissions-only cell
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	for _, part := range [][]*pb.Event{evs[:firstFlush], evs[firstFlush:]} {
		h.send(o, h.seq+1, false, part...)
		h.eng.housekeep(clock.t)
		w.merge(<-w.in)
	}
	w.writeDue(true)
	lines := readDigest(t, w, digestT0.Truncate(time.Hour))
	sizes := map[string]int{"dist": 4, "turn": 4, "gap": 4, "speed": 5, "face": 4, "action": 2}
	for _, c := range []struct {
		check                 string
		acts                  []act
		contexts              []string
		evaluations, failures uint64
	}{
		{"scaffold.face", places, []string{"dist", "turn", "gap", "speed", "face"}, 15, 0},
		{"scaffold.ray", places, []string{"dist", "turn", "gap", "speed", "face"}, 16, 1},
		{"scaffold.cursor", places, []string{"dist", "turn", "gap", "speed", "face"}, 16, 2},
		{"scaffold.reach", places, []string{"dist", "turn", "gap", "speed", "face"}, 16, 0},
		{"dig.ray", digs, []string{"dist", "turn", "gap", "speed", "action"}, 4, 2},
	} {
		l := lineOf(t, lines, c.check)
		if u64(l.Evaluations) != c.evaluations || u64(l.Failures) != c.failures || l.Protocol == nil || *l.Protocol != 763 ||
			l.TickClient == nil || *l.TickClient {
			t.Errorf("%s: %+v", c.check, l)
		}
		want := map[string]digestContextLine{}
		for _, name := range c.contexts {
			want[name] = digestContextLine{Evaluations: make([]uint64, sizes[name]), Failures: make([]uint64, sizes[name])}
		}
		for _, a := range c.acts {
			if c.check == "scaffold.face" && a.ctx[4] == 3 {
				continue // a special face
			}
			for i, name := range c.contexts {
				want[name].Evaluations[a.ctx[i]]++
				if slices.Contains(a.fail, c.check) {
					want[name].Failures[a.ctx[i]]++
				}
			}
		}
		if len(l.Context) != len(c.contexts) {
			t.Errorf("%s: contexts %v", c.check, l.Context)
		}
		for _, name := range c.contexts {
			got := l.Context[name]
			if len(got.Buckets) != sizes[name] || !slices.Equal(got.Evaluations, want[name].Evaluations) || !slices.Equal(got.Failures, want[name].Failures) {
				t.Errorf("%s %s: %+v, want evaluations %v, failures %v", c.check, name, got, want[name].Evaluations, want[name].Failures)
			}
		}
		if cursor := c.check == "scaffold.cursor"; cursor != (l.CursorOnly != nil) || cursor && *l.CursorOnly != 1 {
			t.Errorf("%s: cursor_only %v", c.check, l.CursorOnly)
		}
	}
	if fly := lineOf(t, lines, "movement.fly"); fly.Emissions != 1 || fly.Context != nil || fly.CursorOnly != nil {
		t.Errorf("an emissions-only cell has no context: %+v", fly)
	}

	// The JSON: the context and cursor_only follow the earlier fields, which keep their names.
	raw, err := os.ReadFile(filepath.Join(w.dir, digestName(digestHour(digestT0))))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"context":{"action":{"buckets":["start","finish"],"evaluations":[2,2],"failures":[1,1]},` +
			`"dist":{"buckets":["<1.5","1.5-3","3-4.5",">=4.5"],"evaluations":[0,4,0,0],"failures":[0,2,0,0]},`,
		`"gap":{"buckets":["<60","60-250","250-1000",">=1000"],"evaluations":[1,2,0,1],"failures":[1,1,0,0]},` +
			`"speed":{"buckets":["none","<0.05","0.05-0.2","0.2-0.4",">=0.4"],"evaluations":[0,4,0,0,0],"failures":[0,2,0,0,0]},` +
			`"turn":{"buckets":["0","0-5","5-30",">30"],"evaluations":[3,0,1,0],"failures":[1,0,1,0]}}}`,
		`"face":{"buckets":["up","down","side","other"],"evaluations":[1,1,13,1],"failures":[0,0,2,0]}`,
		`"cursor_only":1,"context":{`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("the digest lacks %s:\n%s", want, raw)
		}
	}
	for _, private := range []string{p.Name, identity.UUID(p.UUID).String(), p.Session} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("the digest holds %q", private)
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["evaluations"]; !ok {
			continue // emissions only
		}
		for _, name := range []string{"hour", "check", "server_id", "protocol", "brand", "tick_client", "pose", "ping",
			"evaluations", "failures", "emissions", "eye_path", "eyes", "rotations", "le", "hist", "context"} {
			if _, ok := fields[name]; !ok {
				t.Errorf("no %q in %s", name, line)
			}
		}
	}
}

// Forwarded local flags count in emissions-only cells, also for a session beyond the player cap (its
// protocol from the join, its brand from the update), and so do the ML signals that are sent; a silent
// batch (warm start) counts none of them.
func TestDigestLocalFlagsAndModelSignals(t *testing.T) {
	clock := &testClock{t: digestT0}
	h := newHarness(t, nil)
	h.env.now = clock.now
	h.env.scorer = fixedScorer{p: 0.95, points: 2}
	w := newTestDigest(t, clock)
	h.env.digest = w
	h.eng = newEngine(3, h.env, nil)
	srv := testkit.NewServerID()
	o := origin(srv, pb.Platform_PLATFORM_PAPER)
	var sigs []*pb.Signal
	send := func(silent bool, evs ...*pb.Event) {
		t.Helper()
		sigs = append(sigs, signals(h.send(o, h.seq+1, silent, evs...))...)
	}
	tracked := testkit.NewPlayer("tracked")
	send(false, tracked.Join(), tracked.State())
	for range 59 { // a free tenant tracks 60 players
		p := testkit.NewPlayer("filler")
		send(false, p.Join(), p.State())
	}
	capped := testkit.NewPlayer("capped")
	capped.Protocol = 47
	brand := capped.Join()
	brand.GetJoin().Update, brand.GetJoin().Brand = true, "Badlion Client"
	send(false, capped.Join(), capped.State(), brand)
	if s := h.eng.sessions[sessKey{testTenant, identity.UUID(capped.UUID), srv}]; s == nil || s.view != nil {
		t.Fatal("fixture: the 61st player is not tracked")
	}
	send(false, flag(tracked, "movement.fly", 1), flag(capped, "movement.fly", 1), flag(capped, "movement.fly", 1))
	send(true, flag(tracked, "movement.fly", 1), flag(capped, "movement.fly", 1)) // a warm start replays them silently
	var moves []*pb.Event
	for i := 0; i < 700; i++ { // a window finalised, another by the quit
		moves = append(moves, tracked.Move(geom.V(0.05, 0, 0), geom.Rotation{Yaw: float64(i % 30)}))
	}
	send(false, moves...)
	send(false, tracked.Quit())

	h.eng.housekeep(clock.t)
	w.merge(<-w.in)
	w.writeDue(true)
	lines := readDigest(t, w, digestT0.Truncate(time.Hour))
	cell := func(check, brand string) digestLine {
		t.Helper()
		var found []digestLine
		for _, l := range lines {
			if l.Check == check && l.Brand == brand {
				found = append(found, l)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s %s: %d lines in %+v", check, brand, len(found), lines)
		}
		return found[0]
	}
	sent := 0
	for _, s := range sigs {
		if s.CheckId == "ml.test" {
			sent++
		}
	}
	for _, c := range []struct {
		check, brand string
		protocol     int32
		emissions    uint64
	}{{"movement.fly", "vanilla", 769, 1}, {"movement.fly", "badlion", 47, 2}, {"ml.test", "vanilla", 769, uint64(sent)}} {
		l := cell(c.check, c.brand)
		if l.Emissions != c.emissions || l.ServerID != srv || l.Protocol == nil || *l.Protocol != c.protocol || l.Evaluations != nil || l.Hist != nil {
			t.Errorf("%s %s: %+v, want %d emissions", c.check, c.brand, l, c.emissions)
		}
	}
	if sent == 0 {
		t.Fatal("fixture: no ML signal sent")
	}
}

func TestDigestHistogramBuckets(t *testing.T) {
	clock := &testClock{t: digestT0}
	w := newTestDigest(t, clock)
	s := newDigestShard(w)
	s.setNow(clock.t)
	v := checks.NewView()
	v.ServerID, v.Protocol, v.PingMs = testkit.NewServerID(), 340, 220
	for _, x := range []float64{0, 0.1, 3, 3.5, 100, math.NaN()} {
		s.Observe(v, checks.Observation{Check: "scaffold.cursor", Value: x, Failed: x > 3, Pose: geom.PoseGliding})
	}
	for _, x := range []float64{-0.02, 0, 0.01, 5} {
		s.Observe(v, checks.Observation{Check: "scaffold.face", Value: x, Pose: geom.PoseSneaking})
	}
	s.flush()
	w.merge(<-w.in)
	w.writeDue(true)
	lines := readDigest(t, w, digestT0.Truncate(time.Hour))
	cursor := lineOf(t, lines, "scaffold.cursor")
	want := make([]uint64, len(cursor.LE)+1)
	for _, i := range []int{0, 1, 7, 8, 17} {
		want[i]++
	}
	if !slices.Equal(cursor.Hist, want) || u64(cursor.Evaluations) != 6 || u64(cursor.Failures) != 2 {
		t.Errorf("cursor %+v, want histogram %v (NaN is evaluated, not bucketed)", cursor, want)
	}
	if cursor.Pose != "swim-glide" || cursor.Ping != "150-300" || *cursor.TickClient || cursor.Brand != "unknown" {
		t.Errorf("cursor dimensions %+v", cursor)
	}
	face := lineOf(t, lines, "scaffold.face")
	zero := slices.Index(face.LE, 0)
	if face.Hist[zero-1] != 1 || face.Hist[zero] != 1 || face.Hist[zero+1] != 1 || face.Hist[len(face.LE)] != 1 || face.Pose != "sneaking" {
		t.Errorf("face %+v: (-0.05, -0.02] holds -0.02, (-0.02, 0] holds 0, (0, 0.02] holds 0.01, the last bucket 5", face)
	}
	for ms, want := range map[uint32]string{0: "<50", 49: "<50", 50: "50-150", 149: "50-150", 150: "150-300", 300: ">300", 900: ">300"} {
		if got := digestPings[digestPing(ms)]; got != want {
			t.Errorf("ping %d: %s, want %s", ms, got, want)
		}
	}
}

// Beyond digestMaxCells an hour's new cells add up in their check's overflow cell; the totals stay
// right and existing cells keep counting.
func TestDigestCellCap(t *testing.T) {
	clock := &testClock{t: digestT0}
	w := newTestDigest(t, clock)
	servers := make([]string, digestMaxCells+52)
	for i := range servers {
		servers[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
	}
	// Two partitions: one fills the hour, the other adds new and known cells.
	for half := range 2 {
		s := newDigestShard(w)
		s.setNow(clock.t)
		v := checks.NewView()
		for i, srv := range servers {
			if i%2 == half || i < 10 {
				v.ServerID = srv
				s.Emitted(v, "aim.snap")
			}
		}
		s.flush()
		w.merge(<-w.in)
	}
	cells := w.pending[digestHour(clock.t)]
	if len(cells) != digestMaxCells+1 {
		t.Fatalf("%d cells", len(cells))
	}
	total, overflow := uint64(0), uint64(0)
	for k, c := range cells {
		total += c.emissions
		if k.overflow {
			overflow = c.emissions
		}
	}
	if total != uint64(len(servers)+10) || overflow != 52 {
		t.Fatalf("total %d, overflow %d", total, overflow)
	}
	w.writeDue(true)
	lines := readDigest(t, w, digestT0.Truncate(time.Hour))
	last := lines[len(lines)-1] // overflow sorts after the check's other cells
	if len(lines) != digestMaxCells+1 || !last.Overflow || last.ServerID != "" || last.Protocol != nil || last.Brand != "" ||
		last.Emissions != overflow {
		t.Fatalf("overflow line %+v", last)
	}
}

// Files: one per UTC hour, appended (a restart, late data), a crash's partial line cut, modes 0640 and
// 0750 whatever existed before, and files older than the retention deleted, nothing else.
func TestDigestFiles(t *testing.T) {
	clock := &testClock{t: digestT0}
	dir := filepath.Join(t.TempDir(), "digests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "digest-20261001T12Z.jsonl") // ended 31 days ago
	kept := filepath.Join(dir, "digest-20261002T14Z.jsonl")
	other := filepath.Join(dir, "digest-notes.txt")
	for _, p := range []string{old, kept, other} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "digest-20200101T00Z.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := openDigest(dir, 30*24*time.Hour, nil, metrics.New(), clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o750 {
		t.Fatalf("directory mode %v", st.Mode().Perm())
	}
	for p, want := range map[string]bool{old: false, kept: true, other: true, filepath.Join(dir, "digest-20200101T00Z.jsonl"): true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", filepath.Base(p), err == nil, want)
		}
	}

	emit := func(at time.Time, check string) {
		s := newDigestShard(w)
		s.setNow(at)
		v := checks.NewView()
		v.ServerID = "11111111-1111-4111-8111-111111111111"
		s.Emitted(v, check)
		s.flush()
		w.merge(<-w.in)
	}
	cur := filepath.Join(dir, "digest-20261101T13Z.jsonl")
	if err := os.WriteFile(cur, []byte(`{"hour":"2026-11-01T13:00:00Z","check":"aim.gcd","emissions":3}`+"\n"+`{"hour":"2026-11-01T13:0`), 0o600); err != nil {
		t.Fatal(err)
	}
	emit(digestT0, "aim.snap")
	emit(digestT0.Add(time.Hour), "aim.snap") // 14:20
	emit(digestT0.Add(time.Hour), "aim.precision")
	clock.t = digestT0.Add(2*time.Hour + 5*time.Minute) // 15:25: both hours are due
	w.writeDue(false)
	emit(digestT0.Add(30*time.Minute), "aim.snap") // late counts for 13:00
	w.writeDue(false)

	// The crash's partial line is gone, the earlier run's line kept, the late counts appended.
	if lines := readDigest(t, w, digestT0.Truncate(time.Hour)); len(lines) != 3 || lines[0].Check != "aim.gcd" || lines[0].Emissions != 3 ||
		lines[1].Check != "aim.snap" || lines[2].Check != "aim.snap" || lines[1].Emissions+lines[2].Emissions != 2 {
		t.Fatalf("13:00 file %+v", lines)
	}
	if st, _ := os.Stat(cur); st.Mode().Perm() != 0o640 {
		t.Fatalf("file mode %v", st.Mode().Perm())
	}
	next := readDigest(t, w, digestT0.Add(time.Hour).Truncate(time.Hour))
	if len(next) != 2 || next[0].Check != "aim.precision" || next[1].Check != "aim.snap" || next[0].Hour != "2026-11-01T14:00:00Z" {
		t.Fatalf("14:00 file %+v", next)
	}

	clock.t = time.Date(2026, 12, 1, 14, 30, 0, 0, time.UTC) // the 13:00 file ended 30 days and 30 minutes ago
	w.lastJanitor = time.Time{}
	w.writeDue(false)
	if _, err := os.Stat(cur); err == nil {
		t.Error("a file beyond the retention must be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "digest-20261101T14Z.jsonl")); err != nil {
		t.Error("a file within the retention must be kept")
	}
}

// Counting into an existing cell allocates nothing.
func TestDigestCountingDoesNotAllocate(t *testing.T) {
	s := newDigestShard(&digestWriter{})
	v := checks.NewView()
	v.ServerID, v.Protocol, v.PingMs = testkit.NewServerID(), 769, 80
	o := checks.Observation{Check: "scaffold.cursor", Value: 3.2, Failed: true, Eyes: 18, Rotations: 5, EyePath: true,
		Dist: 2.5, Turn: 12, Step: 0.2, GapUs: 50_000, RayHit: true}
	s.Observe(v, o)
	s.Emitted(v, "aim.snap")
	if n := testing.AllocsPerRun(100, func() {
		s.Observe(v, o)
		s.Emitted(v, "aim.snap")
	}); n != 0 {
		t.Fatalf("%v allocations per event", n)
	}
}

// The engine never waits for the writer: a full queue or a closed writer drops the counts (and says so).
func TestDigestNeverBlocks(t *testing.T) {
	clock := &testClock{t: digestT0}
	w := newTestDigest(t, clock) // no writer goroutine: nothing drains the queue
	v := checks.NewView()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range digestQueue + 3 {
			s := newDigestShard(w)
			s.Emitted(v, "aim.snap")
			s.flush()
		}
		w.closed.Store(true)
		s := newDigestShard(w)
		s.Emitted(v, "aim.snap")
		s.flush()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("submit blocked")
	}
	if n := drops(t, w.m, "digest_queue"); n != 4 {
		t.Fatalf("dropped %v", n)
	}
	var nilShard *digestShard
	nilShard.setNow(clock.t)
	nilShard.emitted("aim.snap", "", 0, 0)
	nilShard.flush()
}

// PE_DIGEST_DIR starts the process's writer; the engines' sessions report to it and closing the
// dependencies writes the current hour.
func TestDigestFromConfig(t *testing.T) {
	clock := &testClock{t: digestT0}
	dir := filepath.Join(t.TempDir(), "data", "digests")
	d := &app.Deps{Config: &config.Config{Digest: config.Digest{Dir: dir, Retention: 30 * 24 * time.Hour}},
		Log: slog.New(slog.DiscardHandler), Metrics: metrics.New()}
	svc, _ := NewWithOptions(d, Options{Telemetry: &recorder{}, Scorer: fixedScorer{}, Now: clock.now})
	en, err := svc.newEnv(mapTenants{}, mapErasures{})
	if err != nil || en.digest == nil {
		t.Fatalf("digest not started: %v", err)
	}
	en2, err := svc.newEnv(mapTenants{}, mapErasures{})
	if err != nil || en2.digest != en.digest {
		t.Fatal("one writer per process")
	}
	eng := newEngine(0, en, nil)
	o := origin(testkit.NewServerID(), pb.Platform_PLATFORM_PAPER)
	p := testkit.NewPlayer("cfg")
	sb := &pb.StreamBatch{Origin: o, BatchSeq: 1, Events: []*pb.Event{p.Join(), p.State(), flag(p, "movement.fly", 1)}}
	eng.handle(1, sb, nil, false)
	if s := eng.sessions[sessKey{testTenant, identity.UUID(p.UUID), o.ServerId}]; s == nil || s.ctx.Digest == nil || s.view.ServerID != o.ServerId {
		t.Fatal("tracked sessions report to the digest")
	}
	eng.close()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readDigest(t, en.digest, digestT0.Truncate(time.Hour))
	if len(lines) != 1 || lines[0].Check != "movement.fly" || lines[0].Emissions != 1 || lines[0].Brand != "vanilla" {
		t.Fatalf("lines %+v", lines)
	}

	off := &app.Deps{Config: &config.Config{}, Log: slog.New(slog.DiscardHandler), Metrics: metrics.New()}
	svc, _ = NewWithOptions(off, Options{Telemetry: &recorder{}, Scorer: fixedScorer{}})
	if en, err := svc.newEnv(mapTenants{}, mapErasures{}); err != nil || en.digest != nil || newEngine(0, en, nil).digest != nil {
		t.Fatal("no digest without PE_DIGEST_DIR")
	}
}
