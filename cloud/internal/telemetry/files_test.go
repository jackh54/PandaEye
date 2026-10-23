package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	dto "github.com/prometheus/client_model/go"
)

var update = flag.Bool("update", false, "rewrite testdata/windows-20260105T10Z.csv")

// hour0 is the hour of the golden file (ml/tests/test_go_windows.py reads it).
var hour0 = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

const session = "9a8b7c6d-1111-4222-8333-444455556666"

var other = identity.UUID(uuid.MustParse("1c2d3e4f-5555-4666-8777-888899990000"))

// wantHeader spells out the file contract (ml/README.md) instead of deriving it from the code.
var wantHeader = "tenant_id,player,server_id,session_id,window_start_us,window_end_us,client_protocol,tick_client,hot_loaded," +
	"feature_version,worker_unix_us," + strings.Join(ml.FeatureNames(), ",")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func newFiles(t *testing.T, dir string, c *clock, o FileOptions) *Files {
	t.Helper()
	o.Now = c.Now
	if o.FlushInterval == 0 {
		o.FlushInterval = time.Hour // tests flush explicitly
	}
	s, err := NewFiles(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// win is a window of p starting at startUs and written at writtenUs, with every feature at its default.
func win(p identity.UUID, startUs, writtenUs int64) FeatureWindow {
	f := ml.Defaults()
	return FeatureWindow{TenantID: tenant, Player: p, ServerID: server, SessionID: session, WindowStartUs: startUs,
		WindowEndUs: startUs + ml.WindowUs, ClientProtocol: 769, TickClient: true, FeatureVersion: ml.FeatureVersion,
		Features: f[:], WorkerUnixUs: writtenUs}
}

func flush(t *testing.T, s *Files) {
	t.Helper()
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// readFile returns the (decompressed) content of a window file.
func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(p, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if b, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

// rows splits a window file into its header and rows of cells.
func rows(t *testing.T, p string) (string, [][]string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(readFile(t, p)), "\n"), "\n")
	var out [][]string
	for _, l := range lines[1:] {
		out = append(out, strings.Split(l, ","))
	}
	return lines[0], out
}

// players lists the player column of a file's rows.
func players(t *testing.T, p string) []string {
	t.Helper()
	_, rs := rows(t, p)
	var out []string
	for _, r := range rs {
		out = append(out, r[1])
	}
	return out
}

func gzPath(dir string, h time.Time) string { return filepath.Join(dir, csvName(h)+".gz") }

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestFileFormat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ml")
	c := &clock{t: hour0.Add(10 * time.Minute)}
	s := newFiles(t, dir, c, FileOptions{})
	special := []float32{0.1, 1.0 / 3, -1, math.MaxFloat32, math.SmallestNonzeroFloat32, 1e-7, 16777216, math.Nextafter32(1, 2),
		float32(math.Copysign(0, -1)), 2999.999, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))}
	w := win(player, 1_767_607_200_000_000, 1_767_607_232_500_000)
	for i := range w.Features {
		w.Features[i] = special[i%len(special)]
	}
	s.RecordFeatureWindow(w)
	w.Features[0] = 99 // rows are copied when recorded
	bad := win(other, 1_767_607_230_000_000, 1_767_607_262_000_000)
	bad.ServerID, bad.SessionID = strings.ToUpper(server), "not-a-uuid" // stored as the nil UUID, like ClickHouse
	bad.ClientProtocol, bad.TickClient, bad.HotLoaded = -1, false, true
	s.RecordFeatureWindow(bad)
	flush(t, s)

	p := filepath.Join(dir, "windows-20260105T10Z.csv")
	header, rs := rows(t, p)
	if header != wantHeader || len(strings.Split(header, ",")) != 11+ml.NumFeatures {
		t.Fatalf("header %q", header)
	}
	if len(rs) != 2 {
		t.Fatalf("%d rows", len(rs))
	}
	want := []string{tenant, player.String(), server, session, "1767607200000000", "1767607230000000", "769", "1", "0", "1", "1767607232500000"}
	if !slices.Equal(rs[0][:11], want) {
		t.Fatalf("row %v, want %v", rs[0][:11], want)
	}
	nilUUID := uuid.Nil.String()
	if got := rs[1][:11]; got[2] != nilUUID || got[3] != nilUUID || got[6] != "-1" || got[7] != "0" || got[8] != "1" {
		t.Fatalf("row %v", got)
	}
	for i, cell := range rs[0][11:] {
		x := special[i%len(special)]
		// The pandas loader parses a float64 and then casts to float32: both paths must give x back.
		f32, err := strconv.ParseFloat(cell, 32)
		if err != nil {
			t.Fatalf("feature %d: %q: %v", i, cell, err)
		}
		f64, _ := strconv.ParseFloat(cell, 64)
		if math.IsNaN(float64(x)) {
			if cell != "NaN" {
				t.Errorf("feature %d: NaN written as %q", i, cell)
			}
			continue
		}
		if math.Float32bits(float32(f32)) != math.Float32bits(x) || math.Float32bits(float32(f64)) != math.Float32bits(x) {
			t.Errorf("feature %d: %v written as %q", i, x, cell)
		}
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("directory mode %o", m)
	}
	for _, f := range []string{p, filepath.Join(dir, ErasureJournal)} {
		if m := mode(t, f); m != 0o600 {
			t.Errorf("%s mode %o", f, m)
		}
	}
	if s.Dropped() != 0 {
		t.Fatalf("dropped %d", s.Dropped())
	}
}

func TestFloatFormatRoundTrips(t *testing.T) {
	// Every float32 written by appendRow comes back unchanged through float64 parsing (pandas) and
	// float32 parsing.
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 300_000; i++ {
		x := math.Float32frombits(r.Uint32())
		if math.IsNaN(float64(x)) {
			continue
		}
		s := strconv.FormatFloat(float64(x), 'g', -1, 32)
		f64, err := strconv.ParseFloat(s, 64)
		if err != nil || math.Float32bits(float32(f64)) != math.Float32bits(x) {
			t.Fatalf("%08x: %q -> %v (%v)", math.Float32bits(x), s, f64, err)
		}
	}
}

func TestFileGolden(t *testing.T) {
	// testdata/windows-20260105T10Z.csv is what the sink writes for these rows; the Python side reads
	// it with the existing loader (ml/tests/test_go_windows.py).
	dir := t.TempDir()
	c := &clock{t: hour0.Add(30 * time.Minute)}
	s := newFiles(t, dir, c, FileOptions{})
	base := hour0.UnixMicro()
	for k, p := range []identity.UUID{player, other} {
		w := win(p, base+int64(k)*ml.WindowUs, base+int64(k+1)*ml.WindowUs+ml.GraceUs)
		for i := range w.Features {
			lo, hi := ml.FeatureClip(i)
			switch (i + k) % 4 {
			case 0: // keep the default
			case 1:
				w.Features[i] = lo + (hi-lo)/3
			case 2:
				w.Features[i] = math.Nextafter32(hi, lo)
			case 3:
				w.Features[i] = math.Nextafter32(lo, hi)
			}
		}
		if k == 1 {
			w.SessionID, w.ClientProtocol, w.TickClient, w.HotLoaded = "", 767, false, true
		}
		s.RecordFeatureWindow(w)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(dir, "windows-20260105T10Z.csv"))
	path := filepath.Join("testdata", "windows-20260105T10Z.csv")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run %s -update)", err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the sink no longer writes %s (the format is a contract with ml/pandaeye_ml):\n%s", path, got)
	}
}

func TestFileRotation(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: hour0.Add(59 * time.Minute)}
	s := newFiles(t, dir, c, FileOptions{})
	s.RecordFeatureWindow(win(player, hour0.UnixMicro(), hour0.UnixMicro()+ml.WindowUs))
	flush(t, s)
	csv := filepath.Join(dir, csvName(hour0))
	before := readFile(t, csv)

	next := hour0.Add(time.Hour)
	c.Set(next.Add(time.Second))
	s.RecordFeatureWindow(win(other, next.UnixMicro(), next.UnixMicro()+ml.WindowUs))
	flush(t, s)
	eventually(t, "compressed hour", func() bool { return exists(gzPath(dir, hour0)) && !exists(csv) })
	if got := readFile(t, gzPath(dir, hour0)); !bytes.Equal(got, before) {
		t.Fatalf("gzip content differs from the csv:\n%s\nvs\n%s", got, before)
	}
	if m := mode(t, gzPath(dir, hour0)); m != 0o600 {
		t.Errorf("gz mode %o", m)
	}
	// A clock stepping back keeps writing the newer file.
	c.Set(hour0.Add(30 * time.Minute))
	s.RecordFeatureWindow(win(player, next.UnixMicro()+ml.WindowUs, next.UnixMicro()+2*ml.WindowUs))
	flush(t, s)
	if got := players(t, filepath.Join(dir, csvName(next))); !slices.Equal(got, []string{other.String(), player.String()}) {
		t.Fatalf("current hour rows %v", got)
	}
	if exists(filepath.Join(dir, csvName(hour0))) {
		t.Fatal("the clock going back reopened a past hour")
	}
}

// writeGz writes a .csv.gz window file with the given rows.
func writeGz(t *testing.T, p string, ws ...FeatureWindow) {
	t.Helper()
	var csv []byte
	csv = append(csv, wantHeader+"\n"...)
	for _, w := range ws {
		csv = appendRow(csv, w)
	}
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write(csv)
	zw.Close()
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCSV(t *testing.T, p, tail string, ws ...FeatureWindow) {
	t.Helper()
	b := []byte(wantHeader + "\n")
	for _, w := range ws {
		b = appendRow(b, w)
	}
	if err := os.WriteFile(p, append(b, tail...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := hour0.Add(10 * time.Minute)
	h := func(n int) time.Time { return hour0.Add(time.Duration(n) * time.Hour) }
	w := func(p identity.UUID, at time.Time) FeatureWindow {
		return win(p, at.UnixMicro(), at.UnixMicro()+ml.WindowUs)
	}
	partial := tenant + "," + player.String() + ",5f0c6b1e" // a row cut by a crash
	// Past hours: a .csv with a partial last line; an unfinished compression (the .csv is still there); a
	// finished one (the .csv is gone); a past .csv next to a .csv.gz of the same hour (the clock went back).
	writeCSV(t, filepath.Join(dir, csvName(h(-2))), partial, w(player, h(-2)))
	writeCSV(t, filepath.Join(dir, csvName(h(-3))), "", w(player, h(-3)), w(other, h(-3)))
	if err := os.WriteFile(filepath.Join(dir, tmpPrefix+csvName(h(-3))+".gz"), []byte("\x1f\x8bcut"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGz(t, filepath.Join(dir, tmpPrefix+csvName(h(-4))+".gz"), w(other, h(-4)))
	writeGz(t, gzPath(dir, h(-6)), w(player, h(-6)))
	writeCSV(t, filepath.Join(dir, csvName(h(-6))), "", w(other, h(-6)))
	if err := os.WriteFile(filepath.Join(dir, scratchPrefix+csvName(h(-5))+".gz"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The current hour's file and the journal end in a partial line too.
	writeCSV(t, filepath.Join(dir, csvName(hour0)), partial, w(player, hour0))
	if err := os.WriteFile(filepath.Join(dir, ErasureJournal), []byte(`{"tenant_id":"`+tenant+`","player_uu`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newFiles(t, dir, &clock{t: now}, FileOptions{})
	s.RecordFeatureWindow(w(other, hour0.Add(time.Minute)))
	flush(t, s)
	eventually(t, "past hours compressed", func() bool {
		return !exists(filepath.Join(dir, csvName(h(-2)))) && !exists(filepath.Join(dir, csvName(h(-3)))) &&
			!exists(filepath.Join(dir, csvName(h(-6))))
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		hour int
		want []string
	}{{-2, []string{player.String()}}, {-3, []string{player.String(), other.String()}}, {-4, []string{other.String()}},
		{-6, []string{player.String(), other.String()}}} {
		if got := players(t, gzPath(dir, h(c.hour))); !slices.Equal(got, c.want) {
			t.Errorf("hour %d: rows %v, want %v", c.hour, got, c.want)
		}
	}
	if got := players(t, filepath.Join(dir, csvName(hour0))); !slices.Equal(got, []string{player.String(), other.String()}) {
		t.Errorf("current hour: rows %v", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover %s", e.Name())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ErasureJournal)); len(b) != 0 {
		t.Errorf("partial journal line kept: %q", b)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("directory mode %o", m)
	}
}

func TestFileOverflowNeverBlocks(t *testing.T) {
	m := metrics.New()
	dir := t.TempDir()
	c := &clock{t: hour0}
	s, err := openFiles(dir, FileOptions{QueueSize: 2, Metrics: m, Now: c.Now, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing drains the queue yet: recording must still return at once.
	recorded := make(chan struct{})
	go func() {
		for range 10 {
			s.RecordFeatureWindow(win(player, hour0.UnixMicro(), hour0.UnixMicro()))
		}
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordFeatureWindow blocked on a full queue")
	}
	if s.Dropped() != 8 {
		t.Fatalf("dropped %d", s.Dropped())
	}
	var mv dto.Metric
	if err := m.Drops.WithLabelValues(DropOverflow).Write(&mv); err != nil || mv.GetCounter().GetValue() != 8 {
		t.Fatalf("metric %v (%v)", mv.GetCounter().GetValue(), err)
	}
	// Rows that cannot be written to these columns are dropped as failed inserts.
	w := win(player, 0, 0)
	w.Features = w.Features[:3]
	s.RecordFeatureWindow(w)
	w = win(player, 0, 0)
	w.TenantID = "t_x,y"
	s.RecordFeatureWindow(w)
	if err := m.Drops.WithLabelValues(DropInsert).Write(&mv); err != nil || mv.GetCounter().GetValue() != 2 {
		t.Fatalf("insert drops %v (%v)", mv.GetCounter().GetValue(), err)
	}
	s.start()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := players(t, filepath.Join(dir, csvName(hour0))); len(got) != 2 {
		t.Fatalf("Close wrote %d queued rows", len(got))
	}
	s.RecordFeatureWindow(win(player, 0, 0)) // after Close: dropped, no panic
	if s.Dropped() != 11 {
		t.Fatalf("dropped %d", s.Dropped())
	}
}

// journal returns the erasure journal's entries.
func journal(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ErasureJournal))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if l == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("journal line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

// compacted waits until the compactor has handled the whole journal.
func compacted(t *testing.T, dir string) {
	t.Helper()
	eventually(t, "compaction", func() bool {
		j, err1 := os.Stat(filepath.Join(dir, ErasureJournal))
		d, err2 := os.ReadFile(filepath.Join(dir, erasureDone))
		return err1 == nil && err2 == nil && strings.TrimSpace(string(d)) == strconv.FormatInt(j.Size(), 10)
	})
}

func TestFileErasure(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: hour0.Add(5 * time.Minute)}
	s := newFiles(t, dir, c, FileOptions{})
	us := func(d time.Duration) int64 { return hour0.Add(d).UnixMicro() }
	s.RecordFeatureWindow(win(player, us(0), us(time.Minute)))
	s.RecordFeatureWindow(win(other, us(0), us(time.Minute)))
	s.RecordFeatureWindow(win(player, us(time.Minute), us(2*time.Minute)))
	flush(t, s)
	next := hour0.Add(time.Hour)
	c.Set(next.Add(5 * time.Minute))
	s.RecordFeatureWindow(win(player, us(time.Hour), us(time.Hour+time.Minute)))
	s.RecordFeatureWindow(win(other, us(time.Hour), us(time.Hour+time.Minute)))
	flush(t, s)
	eventually(t, "past hour compressed", func() bool { return exists(gzPath(dir, hour0)) })

	erasedAt := next.Add(10 * time.Minute)
	c.Set(erasedAt)
	start := time.Now()
	if err := s.ErasePlayer(context.Background(), tenant, player.String()); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 10*time.Second { // the api allows 30 s; this is one write and fsync

		t.Fatalf("ErasePlayer took %v", took)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ErasureJournal))
	wantLine := `{"tenant_id":"` + tenant + `","player_uuid":"` + player.String() + `","erased_at_us":` +
		strconv.FormatInt(erasedAt.UnixMicro(), 10) + "}\n"
	if string(raw) != wantLine {
		t.Fatalf("journal %q, want %q", raw, wantLine)
	}
	// Late data from before the erasure is not written; data after it is new and kept.
	c.Set(erasedAt.Add(time.Minute))
	s.RecordFeatureWindow(win(player, us(time.Hour+time.Minute), erasedAt.Add(time.Second).UnixMicro())) // window before the erasure
	s.RecordFeatureWindow(win(player, erasedAt.UnixMicro(), erasedAt.UnixMicro()))                       // written at the erasure
	newer := win(player, erasedAt.UnixMicro(), erasedAt.Add(32*time.Second).UnixMicro())
	s.RecordFeatureWindow(newer)
	flush(t, s)
	compacted(t, dir)
	if got := players(t, gzPath(dir, hour0)); !slices.Equal(got, []string{other.String()}) {
		t.Fatalf("past hour after erasure: %v", got)
	}
	// The current hour still holds the row written before the erasure until it is compressed.
	cur := filepath.Join(dir, csvName(next))
	if got := players(t, cur); !slices.Equal(got, []string{player.String(), other.String(), player.String()}) {
		t.Fatalf("current hour: %v", got)
	}
	c.Set(next.Add(time.Hour + time.Minute))
	flush(t, s)
	eventually(t, "erased hour compressed", func() bool { return exists(gzPath(dir, next)) })
	_, rs := rows(t, gzPath(dir, next))
	if len(rs) != 2 || rs[0][1] != other.String() || rs[1][1] != player.String() ||
		rs[1][10] != strconv.FormatInt(newer.WorkerUnixUs, 10) {
		t.Fatalf("compressed hour after erasure: %v", rs)
	}
	if err := s.ErasePlayer(context.Background(), "bad tenant", player.String()); err == nil {
		t.Fatal("invalid tenant accepted")
	}
	if err := s.ErasePlayer(context.Background(), tenant, strings.ToUpper(player.String())); err == nil {
		t.Fatal("non-canonical UUID accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Running the journal again (as after a restart that lost erasures.done) changes nothing.
	before := map[string][]byte{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		before[e.Name()], _ = os.ReadFile(filepath.Join(dir, e.Name()))
	}
	if err := os.Remove(filepath.Join(dir, erasureDone)); err != nil {
		t.Fatal(err)
	}
	c.Set(next.Add(time.Hour + 2*time.Minute))
	s2 := newFiles(t, dir, c, FileOptions{})
	compacted(t, dir)
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	for name, b := range before {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); !bytes.Equal(got, b) {
			t.Errorf("%s changed by a repeated compaction", name)
		}
	}
	if j := journal(t, dir); len(j) != 1 || len(j[0]) != 3 {
		t.Fatalf("journal %v", j)
	}
}

func TestFileErasureSurvivesRestart(t *testing.T) {
	// An erasure journaled just before a crash (compaction not done) is applied at the next start.
	dir := t.TempDir()
	c := &clock{t: hour0.Add(5 * time.Minute)}
	writeGz(t, gzPath(dir, hour0.Add(-time.Hour)), win(player, hour0.Add(-time.Hour).UnixMicro(), hour0.Add(-50*time.Minute).UnixMicro()),
		win(other, hour0.Add(-time.Hour).UnixMicro(), hour0.Add(-50*time.Minute).UnixMicro()))
	writeGz(t, gzPath(dir, hour0.Add(-2*time.Hour)), win(player, hour0.Add(-2*time.Hour).UnixMicro(), hour0.Add(-2*time.Hour).UnixMicro()))
	line := `{"tenant_id":"` + tenant + `","player_uuid":"` + player.String() + `","erased_at_us":` + strconv.FormatInt(hour0.UnixMicro(), 10) + "}\n"
	if err := os.WriteFile(filepath.Join(dir, ErasureJournal), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newFiles(t, dir, c, FileOptions{})
	compacted(t, dir)
	if got := players(t, gzPath(dir, hour0.Add(-time.Hour))); !slices.Equal(got, []string{other.String()}) {
		t.Fatalf("rows %v", got)
	}
	if exists(gzPath(dir, hour0.Add(-2*time.Hour))) {
		t.Fatal("a file with only erased rows must be deleted")
	}
	// The erasure still filters late rows after the restart.
	s.RecordFeatureWindow(win(player, hour0.Add(-time.Minute).UnixMicro(), hour0.Add(time.Minute).UnixMicro()))
	s.RecordFeatureWindow(win(player, hour0.Add(time.Minute).UnixMicro(), hour0.Add(2*time.Minute).UnixMicro()))
	flush(t, s)
	_, rs := rows(t, filepath.Join(dir, csvName(hour0)))
	if len(rs) != 1 || rs[0][4] != strconv.FormatInt(hour0.Add(time.Minute).UnixMicro(), 10) {
		t.Fatalf("rows after restart %v", rs)
	}
	if m := mode(t, filepath.Join(dir, erasureDone)); m != 0o600 {
		t.Errorf("erasures.done mode %o", m)
	}
}

func TestFileRetentionAndByteCap(t *testing.T) {
	dir := t.TempDir()
	now := hour0.Add(10 * time.Minute)
	ago := func(d time.Duration) time.Time { return hour0.Add(-d) }
	hours := []time.Time{ago(100 * 24 * time.Hour), ago(90*24*time.Hour + time.Hour), ago(90*24*time.Hour - time.Hour),
		ago(10 * 24 * time.Hour), ago(2 * time.Hour), ago(time.Hour)}
	for _, h := range hours {
		writeGz(t, gzPath(dir, h), win(player, h.UnixMicro(), h.UnixMicro()), win(other, h.UnixMicro(), h.UnixMicro()))
	}
	cur := filepath.Join(dir, csvName(hour0))
	writeCSV(t, cur, "", win(player, hour0.UnixMicro(), hour0.UnixMicro()))
	size := func(p string) int64 {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	// Room for the current file and the two newest past hours.
	limit := size(cur) + size(gzPath(dir, hours[4])) + size(gzPath(dir, hours[5]))
	s := newFiles(t, dir, &clock{t: now}, FileOptions{Retention: 90 * 24 * time.Hour, MaxBytes: limit})
	eventually(t, "janitor", func() bool { return !exists(gzPath(dir, hours[3])) })
	for i, h := range hours {
		// 0 and 1 ended more than 90 days ago, 2 and 3 are the oldest beyond the byte cap.
		if exists(gzPath(dir, h)) != (i >= 4) {
			t.Errorf("hour %v: exists %v", h, exists(gzPath(dir, h)))
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The file being written is never deleted, even alone above the cap.
	s = newFiles(t, dir, &clock{t: now}, FileOptions{MaxBytes: 1})
	eventually(t, "janitor", func() bool { return !exists(gzPath(dir, hours[5])) })
	if !exists(cur) || exists(gzPath(dir, hours[4])) {
		t.Fatal("byte cap")
	}
	s.Close()
}

func TestNewPicksFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ml")
	d := testkit.NewDeps(t, "PE_EVENTS_MAX_BYTES=256MiB", "PE_FEATURES_DIR="+dir)
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := s.(*Files)
	if !ok {
		t.Fatalf("sink %T", s)
	}
	if again, _ := New(d); again != s {
		t.Fatal("the sink must be shared per process (the api's erasure reaches the worker's files)")
	}
	if f.o.Retention != 90*24*time.Hour || f.o.MaxBytes != 20<<30 {
		t.Fatalf("options %+v", f.o)
	}
	now := time.Now()
	s.RecordFeatureWindow(win(player, now.UnixMicro(), now.UnixMicro()))
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "windows-*Z.csv"))
	if len(files) != 1 || len(players(t, files[0])) != 1 {
		t.Fatalf("d.Close did not flush the files: %v", files)
	}
}
