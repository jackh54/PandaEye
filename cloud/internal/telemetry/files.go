package telemetry

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// File sink defaults (PE_FEATURES_RETENTION, PE_FEATURES_MAX_BYTES; OPERATIONS 3).
const (
	DefaultFileQueueSize     = 10_000 // rows; local writes do not stall like a remote insert
	DefaultFeaturesRetention = 90 * 24 * time.Hour
	DefaultFeaturesMaxBytes  = 20 << 30
	// MaintainInterval is how often the maintenance loop runs without being woken (retention, byte cap,
	// anything a failed run left).
	MaintainInterval = time.Hour
	// ErasureJournal is the erasure journal in the features directory.
	ErasureJournal = "erasures.jsonl"

	erasureDone   = "erasures.done" // journal bytes whose rows are gone from every .csv.gz
	tmpPrefix     = ".tmp-"         // a .csv.gz being compressed (recover finishes or discards it)
	scratchPrefix = ".compact-"     // a rewrite that recover simply deletes
	maxLineBytes  = 1 << 20
)

var windowFileRE = regexp.MustCompile(`^windows-([0-9]{8}T[0-9]{2})Z\.csv(\.gz)?$`)

// metaColumns are the first columns of every window file: the metadata columns of ml/FEATURES.md
// section 5 and worker_unix_us. The feature columns after them change with the feature version.
var metaColumns = []string{"tenant_id", "player", "server_id", "session_id", "window_start_us", "window_end_us",
	"client_protocol", "tick_client", "hot_loaded", "feature_version", "worker_unix_us"}

// metaHeader starts the header of a window file of any feature version.
var metaHeader = []byte(strings.Join(metaColumns, ",") + ",")

// errBadFile marks a window file whose content cannot be read: a corrupt gzip stream, an overlong line
// or a first line that is not a window header. Rewrites keep the rows read before the damage.
var errBadFile = errors.New("damaged window file")

// fileColumns is the header of the files this version writes: metaColumns, then the features in table
// order.
func fileColumns() []string {
	return append(append([]string(nil), metaColumns...), ml.FeatureNames()...)
}

// FileOptions tunes the file sink; zero values take the defaults.
type FileOptions struct {
	Log           *slog.Logger
	Metrics       *metrics.Metrics // drop counters; nil = none
	Retention     time.Duration    // files whose hour ended longer ago are deleted
	MaxBytes      int64            // beyond it the oldest files are deleted
	QueueSize     int
	BatchRows     int // rows are appended in one write every FlushInterval or BatchRows rows
	FlushInterval time.Duration
	Now           func() time.Time
}

func (o FileOptions) withDefaults() FileOptions {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Retention <= 0 {
		o.Retention = DefaultFeaturesRetention
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultFeaturesMaxBytes
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultFileQueueSize
	}
	if o.BatchRows <= 0 {
		o.BatchRows = DefaultBatchRows
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Files is the feature-window file sink (PE_FEATURES_DIR): ML training data without a ClickHouse
// server. The directory holds
//
//	windows-YYYYMMDDTHHZ.csv     the UTC hour being written: the header row, then rows, appended
//	windows-YYYYMMDDTHHZ.csv.gz  a past hour: gzip of its complete csv (the .csv is removed)
//	erasures.jsonl               one {"tenant_id","player_uuid","erased_at_us"} line per erasure
//	erasures.done                the journal length every .csv.gz has been cleaned up to
//
// Each file reads unchanged with ml/pandaeye_ml's CSV loader: values as the ClickHouse sink stores them
// (canonical UUIDs, the nil UUID for an invalid server or session id, 0/1 flags) and floats in the
// shortest form that parses back to the same float32. Signals, verdicts and sessions are not stored:
// they hold more personal data and training does not need them.
//
// One goroutine appends rows; a maintenance goroutine compresses past hours, removes erased rows and
// deletes files beyond the retention or the byte cap.
type Files struct {
	dir    string
	o      FileOptions
	header []byte

	queue     chan FeatureWindow
	reqs      chan fileReq
	wake      chan struct{} // the maintenance loop has work
	stop      chan struct{}
	done      chan struct{} // writer exited
	maintDone chan struct{}
	closing   atomic.Bool
	dropped   atomic.Uint64
	once      sync.Once
	closeErr  error

	mu         sync.Mutex
	erased     erasures  // "tenant_id,player" -> newest erased_at_us
	journalLen int64     // bytes of complete journal lines
	active     time.Time // hour of the file the writer appends to; maintenance never touches it

	// writer goroutine only
	journal *os.File
	cur     *os.File
	curHour time.Time
	curSize int64
	buf     []byte
	pending int

	doneLen int64 // maintenance goroutine only

	logMu   sync.Mutex
	lastLog time.Time
}

var _ Sink = (*Files)(nil)

type fileReq struct {
	erase string // "tenant_id,player"; empty = flush only
	ack   chan error
}

// erasureEntry is one line of the erasure journal.
type erasureEntry struct {
	TenantID   string `json:"tenant_id"`
	PlayerUUID string `json:"player_uuid"`
	ErasedAtUs int64  `json:"erased_at_us"`
}

// erasures maps "tenant_id,player" (the first two columns of a row) to the newest erasure time.
type erasures map[string]int64

// drops reports whether a data line belongs to an erased player and predates the erasure: its window
// started before erased_at_us or it was written at or before it. Later rows are new data (the worker
// treats them the same way, worker/engine.go erased) and are kept.
func (e erasures) drops(line []byte) bool {
	if len(e) == 0 {
		return false
	}
	key, windowStart, written, ok := rowFields(line)
	at, erased := e[string(key)]
	if !erased {
		return false
	}
	return !ok || windowStart < at || written <= at
}

// NewFiles opens (creating it, mode 0700) the features directory, recovers from an interrupted run and
// starts the writer and maintenance loops.
func NewFiles(dir string, o FileOptions) (*Files, error) {
	s, err := openFiles(dir, o)
	if err != nil {
		return nil, err
	}
	s.start()
	return s, nil
}

func openFiles(dir string, o FileOptions) (*Files, error) {
	if dir == "" {
		return nil, errors.New("telemetry: empty features directory")
	}
	o = o.withDefaults()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	s := &Files{dir: dir, o: o, header: []byte(strings.Join(fileColumns(), ",") + "\n"),
		queue: make(chan FeatureWindow, o.QueueSize), reqs: make(chan fileReq), wake: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{}), maintDone: make(chan struct{}), erased: erasures{}}
	if err := s.recover(); err != nil {
		return nil, fmt.Errorf("telemetry: features directory: %w", err)
	}
	j, err := os.OpenFile(filepath.Join(dir, ErasureJournal), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	syncDir(dir) // a new journal must survive a crash after its first fsynced erasure
	s.journal = j
	return s, nil
}

func (s *Files) start() {
	go s.run()
	go s.maintain()
}

// recover prepares the directory after a crash: it finishes or discards interrupted rewrites, cuts a
// partial journal line, loads the erasures and picks the hour to append to (the newest .csv if the clock
// went back). Past hours' .csv files are compressed by the maintenance loop; a partial last line of the
// current one is cut when the writer opens it.
func (s *Files) recover() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	s.active = s.o.Now().UTC().Truncate(time.Hour)
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(s.dir, name)
		// Only the sink's own temporary files are touched: the directory may hold other files.
		switch {
		case strings.HasPrefix(name, scratchPrefix):
			if rest := strings.TrimPrefix(name, scratchPrefix); rest != erasureDone && !windowFileRE.MatchString(rest) {
				continue
			}
			if err := os.Remove(p); err != nil {
				return err
			}
		case strings.HasPrefix(name, tmpPrefix):
			// The temp file is complete once its .csv is gone (compress removes the .csv first).
			gz := strings.TrimPrefix(name, tmpPrefix)
			m := windowFileRE.FindStringSubmatch(gz)
			if m == nil || m[2] == "" {
				continue
			}
			if exists(filepath.Join(s.dir, strings.TrimSuffix(gz, ".gz"))) {
				err = os.Remove(p)
			} else {
				err = os.Rename(p, filepath.Join(s.dir, gz))
			}
			if err != nil {
				return err
			}
		default:
			if h, gz, ok := parseWindowName(name); ok && !gz && h.After(s.active) {
				s.active = h
			}
		}
	}
	s.curHour = s.active
	if err := s.loadJournal(); err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(s.dir, erasureDone)); err == nil {
		s.doneLen, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.doneLen = min(max(s.doneLen, 0), s.journalLen)
	syncDir(s.dir)
	return nil
}

func (s *Files) loadJournal() error {
	p := filepath.Join(s.dir, ErasureJournal)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	n := bytes.LastIndexByte(data, '\n') + 1
	if n < len(data) {
		// A line that was not completely written was never acknowledged; the api retries that erasure.
		if err := os.Truncate(p, int64(n)); err != nil {
			return err
		}
	}
	for i, line := range bytes.Split(data[:n], []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e erasureEntry
		err := json.Unmarshal(line, &e)
		if _, uerr := identity.ParseCanonicalUUID(e.PlayerUUID); err != nil || uerr != nil || !identity.ValidTenantID(e.TenantID) {
			s.o.Log.Error("erasure journal: malformed line ignored", "file", p, "line", i+1)
			continue
		}
		k := e.TenantID + "," + e.PlayerUUID
		if at, ok := s.erased[k]; !ok || e.ErasedAtUs > at {
			s.erased[k] = e.ErasedAtUs
		}
	}
	s.journalLen = int64(n)
	return nil
}

// ---------------------------------------------------------------------------------------------
// Recording (callers) and the writer loop
// ---------------------------------------------------------------------------------------------

// RecordFeatureWindow queues one row without blocking; a full queue (or a closed sink) drops it. Rows
// of another feature version or vector length, or with an invalid tenant id, cannot be written to
// these columns and are dropped as failed inserts.
func (s *Files) RecordFeatureWindow(w FeatureWindow) {
	if int(w.FeatureVersion) != ml.FeatureVersion || len(w.Features) != ml.NumFeatures || !identity.ValidTenantID(w.TenantID) {
		s.drop(DropInsert, 1)
		s.logErr("feature window not written", 1, fmt.Errorf("feature version %d with %d features (tenant valid: %t)",
			w.FeatureVersion, len(w.Features), identity.ValidTenantID(w.TenantID)))
		return
	}
	if s.closing.Load() {
		s.drop(DropOverflow, 1)
		return
	}
	w.Features = append([]float32(nil), w.Features...)
	select {
	case s.queue <- w:
	default:
		s.drop(DropOverflow, 1)
	}
}

// RecordSignal does nothing: the files hold feature windows only.
func (*Files) RecordSignal(*pb.Signal) {}

// RecordVerdict does nothing: the files hold feature windows only.
func (*Files) RecordVerdict(*pb.VerdictRecord) {}

// RecordSession does nothing: the files hold feature windows only.
func (*Files) RecordSession(*pb.SessionMarker, *pb.PlayerJoin) {}

// Dropped returns how many rows were dropped.
func (s *Files) Dropped() uint64 { return s.dropped.Load() }

// Flush writes every queued row now.
func (s *Files) Flush(ctx context.Context) error { return s.request(ctx, "") }

// ErasePlayer writes the queued rows, appends the erasure to the journal (fsynced) and returns. From
// then on the player's rows from before the erasure are not written; the maintenance loop removes
// those already in .csv.gz files right away and the current hour's rows when it compresses that file.
func (s *Files) ErasePlayer(ctx context.Context, tenantID, player string) error {
	if !identity.ValidTenantID(tenantID) {
		return errors.New("telemetry: invalid tenant id")
	}
	if _, err := identity.ParseCanonicalUUID(player); err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	return s.request(ctx, tenantID+","+player)
}

func (s *Files) request(ctx context.Context, erase string) error {
	r := fileReq{erase: erase, ack: make(chan error, 1)}
	select {
	case s.reqs <- r:
	case <-s.done:
		return errors.New("telemetry: sink closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting rows, writes what is queued, stops the maintenance loop (an interrupted
// rewrite is redone at the next start) and waits for both, at most FlushTimeout.
func (s *Files) Close() error {
	s.once.Do(func() {
		s.closing.Store(true)
		close(s.stop)
		t := time.NewTimer(FlushTimeout)
		defer t.Stop()
		for _, ch := range []chan struct{}{s.done, s.maintDone} {
			select {
			case <-ch:
			case <-t.C:
				s.closeErr = errors.New("telemetry: closing the feature files timed out")
				return
			}
		}
		s.closeErr = s.journal.Close()
	})
	return s.closeErr
}

func (s *Files) drop(reason string, n int) {
	if n <= 0 {
		return
	}
	s.dropped.Add(uint64(n))
	if s.o.Metrics != nil {
		s.o.Metrics.Drops.WithLabelValues(reason).Add(float64(n))
	}
}

// logErr logs at most one error per minute.
func (s *Files) logErr(msg string, rows int, err error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if time.Since(s.lastLog) < time.Minute {
		return
	}
	s.lastLog = time.Now()
	s.o.Log.Warn(msg, "dir", s.dir, "rows", rows, "err", err)
}

func (s *Files) run() {
	defer close(s.done)
	drain := func() {
		for {
			select {
			case w := <-s.queue:
				s.add(w)
			default:
				return
			}
		}
	}
	tick := time.NewTicker(s.o.FlushInterval)
	defer tick.Stop()
	for {
		select {
		case w := <-s.queue:
			s.add(w)
		case <-tick.C:
			s.flush()
		case r := <-s.reqs:
			drain()
			s.flush()
			var err error
			if r.erase != "" {
				err = s.journalErasure(r.erase)
			}
			r.ack <- err
		case <-s.stop:
			drain()
			s.flush()
			s.closeCur()
			return
		}
	}
}

// add formats one row into the write buffer unless it is late data of an erased player.
func (s *Files) add(w FeatureWindow) {
	start := len(s.buf)
	s.buf = appendRow(s.buf, w)
	s.mu.Lock()
	erased := s.erased.drops(s.buf[start:])
	s.mu.Unlock()
	if erased {
		s.buf = s.buf[:start]
		return
	}
	s.pending++
	if s.pending >= s.o.BatchRows {
		s.flush()
	}
}

// appendRow formats w as one line of the file columns.
func appendRow(b []byte, w FeatureWindow) []byte {
	b = append(b, w.TenantID...)
	b = append(b, ',')
	b = append(b, w.Player.String()...)
	b = append(b, ',')
	b = append(b, parseUUID(w.ServerID).String()...)
	b = append(b, ',')
	b = append(b, parseUUID(w.SessionID).String()...)
	b = append(b, ',')
	b = strconv.AppendInt(b, w.WindowStartUs, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, w.WindowEndUs, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(w.ClientProtocol), 10)
	b = append(b, ',', '0'+boolByte(w.TickClient), ',', '0'+boolByte(w.HotLoaded), ',')
	b = strconv.AppendUint(b, uint64(w.FeatureVersion), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, w.WorkerUnixUs, 10)
	for _, x := range w.Features {
		b = append(b, ',')
		b = strconv.AppendFloat(b, float64(x), 'g', -1, 32) // shortest form that parses back to x
	}
	return append(b, '\n')
}

// hour is the hour to write now; it never goes back (a clock stepping back keeps the newer file).
func (s *Files) hour() time.Time {
	h := s.o.Now().UTC().Truncate(time.Hour)
	if h.Before(s.curHour) {
		return s.curHour
	}
	return h
}

// flush moves to a new file when the hour changed, then appends the buffered rows in one write. A
// failed write is cut off again (the file keeps whole lines) and its rows are dropped.
func (s *Files) flush() {
	if h := s.hour(); h.After(s.curHour) {
		s.closeCur()
		s.curHour = h
		s.mu.Lock()
		s.active = h
		s.mu.Unlock()
		s.signal() // compress the finished hour
	}
	if s.pending == 0 {
		return
	}
	if err := s.write(s.buf); err != nil {
		s.drop(DropInsert, s.pending)
		s.logErr("feature file write failed", s.pending, err)
	}
	s.buf, s.pending = s.buf[:0], 0
}

func (s *Files) write(b []byte) error {
	if s.cur == nil {
		if err := s.open(); err != nil {
			return err
		}
	}
	if _, err := s.cur.Write(b); err != nil {
		_ = s.cur.Truncate(s.curSize)
		s.closeCur()
		return err
	}
	s.curSize += int64(len(b))
	return nil
}

// open opens the current hour's file for appending: a new file gets the header, an existing one loses
// a partial last line left by a crash. An existing file with another header (written by a version with
// other features, upgraded within the hour) cannot take these rows: it is started over.
func (s *Files) open() error {
	p := filepath.Join(s.dir, csvName(s.curHour))
	f, err := os.OpenFile(p, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	size, err := completeLen(f)
	if err == nil && size > 0 {
		head := make([]byte, len(s.header))
		if n, rerr := f.ReadAt(head, 0); !bytes.Equal(head[:n], s.header) {
			if rerr != nil && rerr != io.EOF {
				f.Close()
				return rerr
			}
			s.o.Log.Warn("feature file of another feature version replaced", "file", p)
			size = 0
		}
	}
	if err == nil {
		err = f.Truncate(size)
	}
	if err == nil && size == 0 {
		_, err = f.Write(s.header)
		size = int64(len(s.header))
		syncDir(s.dir)
	}
	if err != nil {
		f.Close()
		return err
	}
	s.cur, s.curSize = f, size
	return nil
}

func (s *Files) closeCur() {
	if s.cur == nil {
		return
	}
	err := s.cur.Sync()
	if cerr := s.cur.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		s.logErr("feature file close failed", 0, err)
	}
	s.cur = nil
}

// journalErasure appends an erasure to the journal and applies it to every later write.
func (s *Files) journalErasure(key string) error {
	tenant, player, _ := strings.Cut(key, ",")
	at := s.o.Now().UnixMicro()
	line, err := json.Marshal(erasureEntry{TenantID: tenant, PlayerUUID: player, ErasedAtUs: at})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	off := s.journalLen
	s.mu.Unlock()
	if _, err := s.journal.Write(line); err != nil {
		_ = s.journal.Truncate(off)
		return fmt.Errorf("telemetry: erasure journal: %w", err)
	}
	// Applied even if the sync fails: the line is in the file, and the api retries on the error.
	syncErr := s.journal.Sync()
	s.mu.Lock()
	if prev, ok := s.erased[key]; !ok || at > prev {
		s.erased[key] = at
	}
	s.journalLen = off + int64(len(line))
	s.mu.Unlock()
	s.signal()
	if syncErr != nil {
		return fmt.Errorf("telemetry: erasure journal: %w", syncErr)
	}
	return nil
}

func (s *Files) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------------------------
// Maintenance: compression, erasure compaction, retention
// ---------------------------------------------------------------------------------------------

var errStopped = errors.New("telemetry: stopped")

func (s *Files) maintain() {
	defer close(s.maintDone)
	tick := time.NewTicker(MaintainInterval)
	defer tick.Stop()
	for {
		s.compressPast()
		s.compact()
		s.janitor()
		select {
		case <-s.wake:
		case <-tick.C:
		case <-s.stop:
			return
		}
	}
}

func (s *Files) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Files) snapshot() (erasures, int64, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := make(erasures, len(s.erased))
	for k, v := range s.erased {
		e[k] = v
	}
	return e, s.journalLen, s.active
}

// compressPast compresses every .csv of an hour before the one being written.
func (s *Files) compressPast() {
	filter, _, active := s.snapshot()
	files, err := s.list()
	if err != nil {
		s.logErr("feature files: list failed", 0, err)
		return
	}
	for _, f := range files {
		if f.gz || !f.hour.Before(active) || s.stopping() {
			continue
		}
		if err := s.compress(f.hour, filter); err != nil && !errors.Is(err, errStopped) {
			s.logErr("feature file compression failed", 0, err)
		}
	}
}

// compress turns a past hour's .csv into its .csv.gz (merged into an existing .csv.gz of that hour,
// which only a clock going back produces) without erased rows or a partial last line. The order (a
// complete temp file, then the .csv removed, then the rename) lets recover tell a finished temp file
// from an unfinished one.
func (s *Files) compress(hour time.Time, filter erasures) error {
	csvPath := filepath.Join(s.dir, csvName(hour))
	gzPath := csvPath + ".gz"
	tmp := filepath.Join(s.dir, tmpPrefix+filepath.Base(gzPath))
	srcs := []string{csvPath}
	if exists(gzPath) {
		srcs = []string{gzPath, csvPath}
	}
	rows, err := s.rewrite(tmp, srcs, filter)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if rows == 0 {
		os.Remove(tmp)
		err = os.Remove(csvPath)
		if len(srcs) == 2 {
			err = errors.Join(err, os.Remove(gzPath))
		}
		syncDir(s.dir)
		return err
	}
	if err := os.Remove(csvPath); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(s.dir)
	if err := os.Rename(tmp, gzPath); err != nil {
		return err // recover renames it at the next start
	}
	syncDir(s.dir)
	return nil
}

// compact removes erased rows from every .csv.gz once the journal has grown, then records how far the
// journal is done, so a restart resumes an interrupted pass. Running it again changes nothing. A file
// that fails does not hold up the others; the pass is retried at the next run.
func (s *Files) compact() {
	filter, target, _ := s.snapshot()
	if target <= s.doneLen {
		return
	}
	files, err := s.list()
	if err != nil {
		s.logErr("feature files: list failed", 0, err)
		return
	}
	failed := false
	for _, f := range files {
		if !f.gz {
			continue
		}
		if s.stopping() {
			return
		}
		if err := s.compactFile(f.name, filter); err != nil {
			if errors.Is(err, errStopped) {
				return
			}
			s.logErr("feature file erasure failed", 0, err)
			failed = true
		}
	}
	if failed {
		return
	}
	if err := s.writeDone(target); err != nil {
		s.logErr("feature files: erasure progress not saved", 0, err)
		return
	}
	s.doneLen = target
}

func (s *Files) compactFile(name string, filter erasures) error {
	p := filepath.Join(s.dir, name)
	hit, err := s.scan(p, filter)
	if errors.Is(err, errBadFile) {
		hit, err = true, nil // the rewrite keeps the rows before the damage
	}
	if err != nil || !hit {
		return err
	}
	tmp := filepath.Join(s.dir, scratchPrefix+name)
	rows, err := s.rewrite(tmp, []string{p}, filter)
	if err == nil && rows == 0 {
		os.Remove(tmp)
		err = os.Remove(p)
	} else if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(s.dir)
	return nil
}

func (s *Files) writeDone(n int64) error {
	tmp := filepath.Join(s.dir, scratchPrefix+erasureDone)
	if err := writeFileSync(tmp, []byte(strconv.FormatInt(n, 10)+"\n")); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, erasureDone)); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(s.dir)
	return nil
}

// janitor deletes the files whose hour ended more than Retention ago, then the oldest files while all of
// them together exceed MaxBytes. The file being written is never deleted.
func (s *Files) janitor() {
	_, _, active := s.snapshot()
	files, err := s.list()
	if err != nil {
		s.logErr("feature files: list failed", 0, err)
		return
	}
	cutoff := s.o.Now().Add(-s.o.Retention)
	var keep []windowFile
	var total int64
	removed := false
	remove := func(f windowFile) {
		if err := os.Remove(filepath.Join(s.dir, f.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logErr("feature file not deleted", 0, err)
			return
		}
		removed = true
	}
	for _, f := range files {
		current := !f.gz && !f.hour.Before(active) // the writer may have moved on since the snapshot
		if !current && !f.hour.Add(time.Hour).After(cutoff) {
			remove(f)
			continue
		}
		keep = append(keep, f)
		total += f.size
	}
	for _, f := range keep { // oldest first
		if total <= s.o.MaxBytes {
			break
		}
		if !f.gz && !f.hour.Before(active) {
			continue
		}
		remove(f)
		total -= f.size
	}
	if removed {
		syncDir(s.dir)
	}
}

type windowFile struct {
	name string
	hour time.Time
	gz   bool
	size int64
}

// list returns the window files, oldest hour first (a .csv before the .csv.gz of the same hour).
func (s *Files) list() ([]windowFile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []windowFile
	for _, e := range entries {
		h, gz, ok := parseWindowName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // deleted meanwhile
		}
		out = append(out, windowFile{name: e.Name(), hour: h, gz: gz, size: info.Size()})
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].hour.Equal(out[b].hour) {
			return out[a].hour.Before(out[b].hour)
		}
		return !out[a].gz && out[b].gz
	})
	return out, nil
}

// rewrite writes the header of the first source and every kept data row of srcs, in order,
// gzip-compressed to dst (mode 0600, fsynced). It returns the number of rows written. Of a damaged
// source (errBadFile) only the rows before the damage are kept, and a source with another header (the
// hour's files of two feature versions, after a clock going back) is left out: neither can hold up
// compression or an erasure.
func (s *Files) rewrite(dst string, srcs []string, filter erasures) (int, error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 256<<10)
	zw := gzip.NewWriter(bw)
	rows := 0
	var header []byte
	for _, src := range srcs {
		err = s.eachRow(src, func(h []byte) error {
			if header == nil {
				header = append([]byte(nil), h...)
				_, err := zw.Write(header)
				return err
			}
			if !bytes.Equal(h, header) {
				return fmt.Errorf("%s: %w: another header than %s", filepath.Base(src), errBadFile, filepath.Base(srcs[0]))
			}
			return nil
		}, func(line []byte) error {
			if filter.drops(line) {
				return nil
			}
			rows++
			_, err := zw.Write(line)
			return err
		})
		if errors.Is(err, errBadFile) {
			s.o.Log.Error("damaged feature file: only the rows before the damage are kept", "dir", s.dir, "err", err)
			err = nil
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	return rows, err
}

// scan reports whether src holds a row that filter drops.
func (s *Files) scan(src string, filter erasures) (bool, error) {
	errHit := errors.New("hit")
	err := s.eachRow(src, nil, func(line []byte) error {
		if filter.drops(line) {
			return errHit
		}
		return nil
	})
	if errors.Is(err, errHit) {
		return true, nil
	}
	return false, err
}

// eachRow calls header with the first line of a .csv or .csv.gz window file (nil header: not called),
// then fn with every complete data line (with its newline). The header may be of any feature version:
// it only has to start with metaColumns. A missing file has no rows. Content that cannot be read fails
// with errBadFile after the rows before it.
func (s *Files) eachRow(src string, header, fn func(line []byte) error) error {
	f, err := os.Open(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(src, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return readErr(src, err)
		}
		defer zr.Close()
		r = zr
	}
	br := bufio.NewReaderSize(r, maxLineBytes)
	for n := 0; ; n++ {
		line, err := br.ReadSlice('\n')
		if err == io.EOF {
			return nil // a partial last line (a crash while appending) is not a row
		}
		if err != nil {
			return readErr(src, err)
		}
		if n == 0 {
			if !bytes.HasPrefix(line, metaHeader) {
				return fmt.Errorf("%s: %w: not a window header", filepath.Base(src), errBadFile)
			}
			if header != nil {
				if err := header(line); err != nil {
					return err
				}
			}
			continue
		}
		if n%4096 == 0 && s.stopping() {
			return errStopped
		}
		if err := fn(line); err != nil {
			return err
		}
	}
}

// readErr wraps an error reading src. One that does not come from the operating system is about the
// content (a corrupt or truncated gzip stream, an overlong line) and wraps errBadFile.
func readErr(src string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", filepath.Base(src), err)
	}
	return fmt.Errorf("%s: %w: %w", filepath.Base(src), errBadFile, err)
}

// rowFields returns a data line's "tenant_id,player" prefix, window_start_us and worker_unix_us (ok is
// false when the numbers do not parse).
func rowFields(line []byte) (key []byte, windowStart, written int64, ok bool) {
	var fields [11][]byte
	rest := line
	for i := range fields {
		j := bytes.IndexByte(rest, ',')
		if j < 0 {
			if i < 2 {
				return nil, 0, 0, false
			}
			break
		}
		fields[i], rest = rest[:j], rest[j+1:]
	}
	key = line[:len(fields[0])+1+len(fields[1])]
	windowStart, ok1 := parseInt(fields[4])
	written, ok2 := parseInt(fields[10])
	return key, windowStart, written, ok1 && ok2
}

func parseInt(b []byte) (int64, bool) {
	neg := len(b) > 0 && b[0] == '-'
	if neg {
		b = b[1:]
	}
	if len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	var n int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

// completeLen returns the length of f up to and including its last newline.
func completeLen(f *os.File) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := st.Size()
	buf := make([]byte, 64<<10)
	for end > 0 {
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

func csvName(hour time.Time) string { return "windows-" + hour.UTC().Format("20060102T15") + "Z.csv" }

func parseWindowName(name string) (hour time.Time, gz, ok bool) {
	m := windowFileRE.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false, false
	}
	h, err := time.Parse("20060102T15", m[1])
	if err != nil {
		return time.Time{}, false, false
	}
	return h, m[2] != "", true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeFileSync(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// syncDir makes renames and removals in dir durable (best effort: training data, not the store).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
