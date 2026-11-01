package worker

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
)

// Check-health digest (PE_DIGEST_DIR; CHECKS.md 5, OPERATIONS 3): hourly counts per cell of check, server,
// client protocol and brand family, for the world-interaction checks also tick client, pose and ping
// class. Each partition engine counts into its own shard without locking; housekeeping hands the shard to
// the process's writer every second without blocking (a full queue drops it), and the writer appends each
// hour to digest-YYYYMMDDTHHZ.jsonl a minute after the hour ended. Nothing about a player is written: no
// name, UUID, session id, IP address, coordinates or check info text.
const (
	digestMaxCells  = 2048             // cells per hour (and per shard flush); more go to their check's overflow cell
	digestQueue     = 256              // shard flushes waiting for the writer
	digestGrace     = time.Minute      // an hour is written this long after it ended (every partition has flushed it)
	digestTick      = 10 * time.Second // how often the writer looks for finished hours
	digestCloseWait = 2 * time.Second  // Close waits this long for the last writes
)

var digestFileRE = regexp.MustCompile(`^digest-([0-9]{8}T[0-9]{2})Z\.jsonl$`)

// digestKey is a cell of one hour.
type digestKey struct {
	hour     int64 // Unix seconds of the UTC hour
	check    string
	detail   bool // a world-interaction cell: evaluations and a histogram (otherwise emissions only)
	overflow bool // the check's cells beyond digestMaxCells; the other dimensions are empty
	server   string
	protocol int32
	brand    checks.BrandFamily
	tick     bool
	pose     uint8 // digestStanding...
	ping     uint8 // digestPing50...
}

type digestCounts struct {
	evaluations, failures, emissions uint64
	eyePath, eyes, rotations         uint64
	hist                             [checks.DigestBuckets]uint64
}

func (c *digestCounts) add(o *digestCounts) {
	c.evaluations += o.evaluations
	c.failures += o.failures
	c.emissions += o.emissions
	c.eyePath += o.eyePath
	c.eyes += o.eyes
	c.rotations += o.rotations
	for i, n := range o.hist {
		c.hist[i] += n
	}
}

// digestCells holds at most digestMaxCells cells; further ones count in their check's overflow cell.
type digestCells map[digestKey]*digestCounts

func (m digestCells) at(k digestKey) *digestCounts {
	if c := m[k]; c != nil {
		return c
	}
	if len(m) >= digestMaxCells && !k.overflow {
		k = digestKey{hour: k.hour, check: k.check, detail: k.detail, overflow: true}
		if c := m[k]; c != nil {
			return c
		}
	}
	c := &digestCounts{}
	m[k] = c
	return c
}

// Pose classes (the eye the plugin reported) and ping classes (View.PingMs) of the world-interaction cells.
const (
	digestStanding uint8 = 1 + iota
	digestSneaking
	digestSwimGlide
)

const (
	digestPing50 uint8 = 1 + iota
	digestPing150
	digestPing300
	digestPingHigh
)

var (
	digestPoses = [...]string{"", "standing", "sneaking", "swim-glide"}
	digestPings = [...]string{"", "<50", "50-150", "150-300", ">300"}
)

func digestPose(p geom.Pose) uint8 {
	switch p {
	case geom.PoseStanding:
		return digestStanding
	case geom.PoseSneaking:
		return digestSneaking
	}
	return digestSwimGlide // swimming, gliding, crawling: the 0.4 eye
}

// digestPing classes a ping; a session without a round-trip sample yet (0) counts as < 50 ms.
func digestPing(ms uint32) uint8 {
	switch {
	case ms < 50:
		return digestPing50
	case ms < 150:
		return digestPing150
	case ms < 300:
		return digestPing300
	}
	return digestPingHigh
}

func digestHour(t time.Time) int64 { return t.Truncate(time.Hour).Unix() }

// ---------------------------------------------------------------------------------------------
// Shards (one per partition engine)
// ---------------------------------------------------------------------------------------------

// digestShard is one partition engine's part of the digest: the counts since its last flush, owned by the
// goroutine driving the engine (no locking). It is the checks.DigestSink of the engine's sessions. A nil
// shard (no digest) ignores everything.
type digestShard struct {
	w     *digestWriter
	hour  int64
	cells digestCells
}

var _ checks.DigestSink = (*digestShard)(nil)

func newDigestShard(w *digestWriter) *digestShard {
	if w == nil {
		return nil
	}
	return &digestShard{w: w}
}

// setNow sets the hour (the worker's clock) of the following counts.
func (s *digestShard) setNow(t time.Time) {
	if s != nil {
		s.hour = digestHour(t)
	}
}

func (s *digestShard) cell(k digestKey) *digestCounts {
	if s.cells == nil {
		s.cells = digestCells{}
	}
	k.hour = s.hour
	return s.cells.at(k)
}

func (s *digestShard) Observe(v *checks.View, o checks.Observation) {
	c := s.cell(digestKey{check: o.Check, detail: true, server: v.ServerID, protocol: v.Protocol, brand: v.BrandFamily,
		tick: v.TickClient, pose: digestPose(o.Pose), ping: digestPing(v.PingMs)})
	c.evaluations++
	if o.Failed {
		c.failures++
	}
	if o.Emitted {
		c.emissions++
	}
	if o.EyePath {
		c.eyePath++
	}
	c.eyes += uint64(max(o.Eyes, 0))
	c.rotations += uint64(max(o.Rotations, 0))
	if edges := checks.DigestEdges(o.Check); edges != nil && !math.IsNaN(o.Value) {
		c.hist[checks.DigestBucket(edges, o.Value)]++
	}
}

func (s *digestShard) Emitted(v *checks.View, check string) {
	s.emitted(check, v.ServerID, v.Protocol, v.BrandFamily)
}

// emitted counts an emission of a check without observations (also a forwarded local flag).
func (s *digestShard) emitted(check, server string, protocol int32, brand checks.BrandFamily) {
	if s != nil {
		s.cell(digestKey{check: check, server: server, protocol: protocol, brand: brand}).emissions++
	}
}

// flush hands the counts to the writer.
func (s *digestShard) flush() {
	if s == nil || len(s.cells) == 0 {
		return
	}
	s.w.submit(s.cells)
	s.cells = nil
}

// ---------------------------------------------------------------------------------------------
// Writer (one per process)
// ---------------------------------------------------------------------------------------------

// digestWriter merges the shards' counts by hour and appends finished hours to their files. Files are
// mode 0640 in a 0750 directory and deleted once their hour ended longer than the retention ago.
type digestWriter struct {
	dir       string
	retention time.Duration
	log       *slog.Logger
	m         *metrics.Metrics // drop counters; nil = none
	now       func() time.Time

	in       chan digestCells
	stop     chan struct{}
	done     chan struct{}
	closed   atomic.Bool
	once     sync.Once
	closeErr error

	// writer goroutine only
	pending     map[int64]digestCells // by hour
	lastJanitor time.Time
	lastLog     time.Time
}

// sharedDigest returns the process's digest writer for PE_DIGEST_DIR (several worker services of one
// process share it); d.Close closes it after the services stopped.
func sharedDigest(d *app.Deps, now func() time.Time) (*digestWriter, error) {
	w, err := d.Shared("digest", func() (any, error) {
		w, err := newDigestWriter(d.Config.Digest.Dir, d.Config.Digest.Retention, d.Log.With("component", "digest"), d.Metrics, now)
		if err != nil {
			return nil, err
		}
		d.OnClose(w.Close)
		return w, nil
	})
	if err != nil {
		return nil, err
	}
	return w.(*digestWriter), nil
}

// newDigestWriter opens (creating it) the digest directory, deletes files beyond the retention and starts
// the writer.
func newDigestWriter(dir string, retention time.Duration, log *slog.Logger, m *metrics.Metrics, now func() time.Time) (*digestWriter, error) {
	w, err := openDigest(dir, retention, log, m, now)
	if err != nil {
		return nil, err
	}
	go w.run()
	return w, nil
}

func openDigest(dir string, retention time.Duration, log *slog.Logger, m *metrics.Metrics, now func() time.Time) (*digestWriter, error) {
	if dir == "" {
		return nil, errors.New("worker: empty digest directory")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("worker: digest: %w", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, fmt.Errorf("worker: digest: %w", err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	w := &digestWriter{dir: dir, retention: retention, log: log, m: m, now: now, in: make(chan digestCells, digestQueue),
		stop: make(chan struct{}), done: make(chan struct{}), pending: map[int64]digestCells{}}
	w.janitor()
	return w, nil
}

// submit queues a shard's counts without blocking; a full queue (or a closed writer) drops them.
func (w *digestWriter) submit(c digestCells) {
	if !w.closed.Load() {
		select {
		case w.in <- c:
			return
		default:
		}
	}
	w.drop("digest_queue", len(c))
}

// Close stops accepting counts, writes everything pending (the current hour too: the next process appends
// the rest of it) and waits for the writer, at most digestCloseWait.
func (w *digestWriter) Close() error {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.stop)
		select {
		case <-w.done:
		case <-time.After(digestCloseWait):
			w.closeErr = errors.New("worker: closing the digest timed out")
		}
	})
	return w.closeErr
}

func (w *digestWriter) run() {
	defer close(w.done)
	tick := time.NewTicker(digestTick)
	defer tick.Stop()
	for {
		select {
		case c := <-w.in:
			w.merge(c)
		case <-tick.C:
			w.writeDue(false)
		case <-w.stop:
			for drained := false; !drained; {
				select {
				case c := <-w.in:
					w.merge(c)
				default:
					drained = true
				}
			}
			w.writeDue(true)
			return
		}
	}
}

func (w *digestWriter) merge(c digestCells) {
	for k, n := range c {
		cells := w.pending[k.hour]
		if cells == nil {
			cells = digestCells{}
			w.pending[k.hour] = cells
		}
		cells.at(k).add(n)
	}
}

// writeDue appends every pending hour that ended at least digestGrace ago (all: every pending hour) to
// its file, then runs the retention janitor once an hour. Counts that arrive for an hour already written
// (a partition that flushed late) are appended as further lines.
func (w *digestWriter) writeDue(all bool) {
	now := w.now()
	hours := make([]int64, 0, len(w.pending))
	for h := range w.pending {
		hours = append(hours, h)
	}
	slices.Sort(hours)
	for _, h := range hours {
		if !all && now.Before(time.Unix(h, 0).Add(time.Hour+digestGrace)) {
			continue
		}
		cells := w.pending[h]
		delete(w.pending, h)
		if err := w.write(h, cells); err != nil {
			w.drop("digest_write", len(cells))
			w.logErr("digest file write failed", err)
		}
	}
	if now.Sub(w.lastJanitor) >= time.Hour {
		w.janitor()
	}
}

// write appends the hour's cells to its file, one JSON line per cell. A partial last line (a crash while
// appending) is cut first, and a failed write is cut off again, so the file holds whole lines only.
func (w *digestWriter) write(hour int64, cells digestCells) error {
	data, err := digestLines(hour, cells)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, digestName(hour)), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return err
	}
	size, err := wholeLines(f)
	if err == nil {
		err = f.Truncate(size)
	}
	if err == nil {
		err = f.Chmod(0o640) // whatever the umask
	}
	if err == nil {
		if _, err = f.Write(data); err != nil {
			_ = f.Truncate(size)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// janitor deletes the digest files whose hour ended more than the retention ago; other files in the
// directory are never touched.
func (w *digestWriter) janitor() {
	now := w.now()
	w.lastJanitor = now
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		w.logErr("digest directory not readable", err)
		return
	}
	cutoff := now.Add(-w.retention)
	for _, e := range entries {
		h, ok := parseDigestName(e.Name())
		if !ok || !e.Type().IsRegular() || h.Add(time.Hour).After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(w.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			w.logErr("digest file not deleted", err)
		}
	}
}

func (w *digestWriter) drop(reason string, n int) {
	if w.m != nil && n > 0 {
		w.m.Drops.WithLabelValues(reason).Add(float64(n))
	}
}

// logErr logs at most one error per minute (writer goroutine only).
func (w *digestWriter) logErr(msg string, err error) {
	if now := time.Now(); now.Sub(w.lastLog) >= time.Minute {
		w.lastLog = now
		w.log.Warn(msg, "dir", w.dir, "err", err)
	}
}

// digestLine is one line of a digest file. Cells of checks without observations have no evaluations,
// candidates or histogram; overflow cells have no dimensions.
type digestLine struct {
	Hour        string    `json:"hour"`
	Check       string    `json:"check"`
	Overflow    bool      `json:"overflow,omitempty"`
	ServerID    string    `json:"server_id,omitempty"`
	Protocol    *int32    `json:"protocol,omitempty"`
	Brand       string    `json:"brand,omitempty"`
	TickClient  *bool     `json:"tick_client,omitempty"`
	Pose        string    `json:"pose,omitempty"`
	Ping        string    `json:"ping,omitempty"`
	Evaluations *uint64   `json:"evaluations,omitempty"`
	Failures    *uint64   `json:"failures,omitempty"`
	Emissions   uint64    `json:"emissions"`
	EyePath     *uint64   `json:"eye_path,omitempty"`
	Eyes        *uint64   `json:"eyes,omitempty"`
	Rotations   *uint64   `json:"rotations,omitempty"`
	LE          []float64 `json:"le,omitempty"`
	Hist        []uint64  `json:"hist,omitempty"`
}

// digestLines formats the cells of an hour, sorted by their dimensions.
func digestLines(hour int64, cells digestCells) ([]byte, error) {
	keys := make([]digestKey, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b digestKey) int {
		return cmp.Or(cmp.Compare(a.check, b.check), cmpBool(a.overflow, b.overflow), cmpBool(a.detail, b.detail),
			cmp.Compare(a.server, b.server), cmp.Compare(a.protocol, b.protocol), cmp.Compare(a.brand, b.brand),
			cmpBool(a.tick, b.tick), cmp.Compare(a.pose, b.pose), cmp.Compare(a.ping, b.ping))
	})
	at := time.Unix(hour, 0).UTC().Format(time.RFC3339)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // the ping classes "<50" and ">300" stay readable
	for _, k := range keys {
		c := cells[k]
		l := digestLine{Hour: at, Check: k.check, Overflow: k.overflow, Emissions: c.emissions}
		if !k.overflow {
			protocol, tick := k.protocol, k.tick
			l.ServerID, l.Protocol, l.Brand = k.server, &protocol, k.brand.String()
			if k.detail {
				l.TickClient, l.Pose, l.Ping = &tick, digestPoses[k.pose], digestPings[k.ping]
			}
		}
		if k.detail {
			l.Evaluations, l.Failures, l.EyePath, l.Eyes, l.Rotations = &c.evaluations, &c.failures, &c.eyePath, &c.eyes, &c.rotations
			if edges := checks.DigestEdges(k.check); edges != nil {
				l.LE, l.Hist = edges, c.hist[:len(edges)+1]
			}
		}
		if err := enc.Encode(l); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// wholeLines returns the length of f up to and including its last newline.
func wholeLines(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	buf := make([]byte, 4096)
	for end := st.Size(); end > 0; {
		n := min(int64(len(buf)), end)
		if _, err := f.ReadAt(buf[:n], end-n); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return end - n + int64(i) + 1, nil
		}
		end -= n
	}
	return 0, nil
}

func digestName(hour int64) string {
	return "digest-" + time.Unix(hour, 0).UTC().Format("20060102T15") + "Z.jsonl"
}

func parseDigestName(name string) (time.Time, bool) {
	m := digestFileRE.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	h, err := time.Parse("20060102T15", m[1])
	return h, err == nil
}
