package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Defaults (ARCHITECTURE 5.7: batched inserts every 1 s or 10 000 rows, drop on overflow).
const (
	DefaultQueueSize     = 100_000
	DefaultBatchRows     = 10_000
	DefaultFlushInterval = time.Second
	FlushTimeout         = 5 * time.Second  // Close waits this long for the final flush
	InsertTimeout        = 10 * time.Second // one batch insert
	DefaultDatabase      = "pandaeye"
	maxBrandLen          = 64
)

// Drop reasons (label "reason" of pandaeye_dropped_total).
const (
	DropOverflow = "telemetry_overflow"
	DropInsert   = "telemetry_insert"
)

// Options tunes the ClickHouse sink; zero values take the defaults.
type Options struct {
	Log           *slog.Logger
	Metrics       *metrics.Metrics // drop counters; nil = none
	Database      string           // default: the DSN's database, else DefaultDatabase
	QueueSize     int
	BatchRows     int
	FlushInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	if o.BatchRows <= 0 {
		o.BatchRows = DefaultBatchRows
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	return o
}

// table identifies one ClickHouse table of deploy/clickhouse/init.sql.
type table int

const (
	tableSignals table = iota
	tableVerdicts
	tableSessions
	tableFeatures
	numTables
)

// tableDefs lists each table's name and the columns the sink inserts, in Append order. Columns with
// a DEFAULT that the sink never sets (feature_windows.time, inserted_at) are left out.
var tableDefs = [numTables]struct {
	name string
	cols []string
}{
	tableSignals: {"signals", []string{"tenant_id", "time", "player", "player_name", "server_id", "server_name", "network_id",
		"session_id", "check_id", "category", "points", "confidence", "event_time_us", "gateway_recv_unix_us", "worker_unix_us",
		"info", "data", "clip_id", "local", "replayed", "hot_loaded", "shadow_mode", "platform"}},
	tableVerdicts: {"verdicts", []string{"tenant_id", "time", "verdict_id", "player", "player_name", "server_id", "network_id",
		"session_id", "action", "category", "score", "confidence", "checks", "mitigations", "strength", "shadow", "ban_duration_s",
		"supersedes", "reason", "clip_ids", "reputation_eligible"}},
	tableSessions: {"sessions", []string{"tenant_id", "time", "player", "player_name", "kind", "session_id", "session_start_us",
		"server_id", "server_name", "network_id", "network_session", "platform", "hot_loaded", "exempt", "shadow_mode",
		"client_protocol", "client_version", "brand", "bedrock"}},
	tableFeatures: {"feature_windows", []string{"tenant_id", "player", "server_id", "session_id", "window_start_us", "window_end_us",
		"client_protocol", "tick_client", "hot_loaded", "feature_version", "features", "worker_unix_us"}},
}

// conn is the part of a ClickHouse connection the sink uses (a fake in tests).
type conn interface {
	PrepareBatch(ctx context.Context, query string) (batch, error)
	Exec(ctx context.Context, query string, args ...any) error
	Close() error
}

// batch is the part of driver.Batch the sink uses.
type batch interface {
	Append(v ...any) error
	Send() error
	Abort() error
}

type driverConn struct{ c driver.Conn }

func (d driverConn) PrepareBatch(ctx context.Context, q string) (batch, error) {
	return d.c.PrepareBatch(ctx, q)
}
func (d driverConn) Exec(ctx context.Context, q string, args ...any) error {
	return d.c.Exec(ctx, q, args...)
}
func (d driverConn) Close() error { return d.c.Close() }

type row struct {
	t    table
	vals []any
}

// ClickHouse is the batching ClickHouse sink.
type ClickHouse struct {
	conn conn
	db   string
	o    Options

	queue    chan row
	flushReq chan chan struct{}
	stop     chan struct{}
	done     chan struct{}
	closing  atomic.Bool
	dropped  atomic.Uint64
	once     sync.Once
	closeErr error

	baseCtx    context.Context // cancelled when Close gives up waiting
	cancelBase context.CancelFunc

	logMu   sync.Mutex
	lastLog time.Time
}

var _ Sink = (*ClickHouse)(nil)

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewClickHouse connects to ClickHouse (clickhouse-go native or HTTP DSN, e.g.
// clickhouse://user:pass@host:9000/pandaeye) and starts the insert loop. An unreachable server is
// logged, not fatal: telemetry is optional and inserts are retried with every batch.
func NewClickHouse(dsn string, o Options) (*ClickHouse, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("telemetry: invalid PE_CLICKHOUSE_DSN") // the DSN may carry a password
	}
	if o.Database == "" {
		o.Database = opts.Auth.Database
	}
	c, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("telemetry: open clickhouse: %w", err)
	}
	s, err := newClickHouse(driverConn{c}, o)
	if err != nil {
		c.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		s.o.Log.Warn("clickhouse unreachable at start; telemetry rows are dropped until it is back", "err", err)
	}
	return s, nil
}

func newClickHouse(c conn, o Options) (*ClickHouse, error) {
	o = o.withDefaults()
	db := o.Database
	if db == "" {
		db = DefaultDatabase
	}
	if !identRE.MatchString(db) {
		return nil, fmt.Errorf("telemetry: invalid database name %q", db)
	}
	s := &ClickHouse{conn: c, db: db, o: o, queue: make(chan row, o.QueueSize), flushReq: make(chan chan struct{}),
		stop: make(chan struct{}), done: make(chan struct{})}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	go s.run()
	return s, nil
}

func (s *ClickHouse) insertQuery(t table) string {
	d := tableDefs[t]
	return "INSERT INTO " + s.db + "." + d.name + " (" + strings.Join(d.cols, ", ") + ")"
}

// enqueue adds a row without blocking; a full queue (or a closed sink) drops it.
func (s *ClickHouse) enqueue(t table, vals []any) {
	if s.closing.Load() {
		s.drop(DropOverflow, 1)
		return
	}
	select {
	case s.queue <- row{t, vals}:
	default:
		s.drop(DropOverflow, 1)
	}
}

func (s *ClickHouse) drop(reason string, n int) {
	if n <= 0 {
		return
	}
	s.dropped.Add(uint64(n))
	if s.o.Metrics != nil {
		s.o.Metrics.Drops.WithLabelValues(reason).Add(float64(n))
	}
}

// Dropped returns how many rows were dropped.
func (s *ClickHouse) Dropped() uint64 { return s.dropped.Load() }

func (s *ClickHouse) run() {
	defer close(s.done)
	var bufs [numTables][][]any
	flushAll := func() {
		for t := range bufs {
			if len(bufs[t]) > 0 {
				s.insert(table(t), bufs[t])
				bufs[t] = nil
			}
		}
	}
	drain := func() {
		for {
			select {
			case r := <-s.queue:
				bufs[r.t] = append(bufs[r.t], r.vals)
				if len(bufs[r.t]) >= s.o.BatchRows {
					s.insert(r.t, bufs[r.t])
					bufs[r.t] = nil
				}
			default:
				return
			}
		}
	}
	tick := time.NewTicker(s.o.FlushInterval)
	defer tick.Stop()
	for {
		select {
		case r := <-s.queue:
			bufs[r.t] = append(bufs[r.t], r.vals)
			if len(bufs[r.t]) >= s.o.BatchRows {
				s.insert(r.t, bufs[r.t])
				bufs[r.t] = nil
			}
		case <-tick.C:
			flushAll()
		case ack := <-s.flushReq:
			drain()
			flushAll()
			close(ack)
		case <-s.stop:
			drain()
			flushAll()
			return
		}
	}
}

// insert sends rows as one batch; on any error the batch is dropped and counted.
func (s *ClickHouse) insert(t table, rows [][]any) {
	ctx, cancel := context.WithTimeout(s.baseCtx, InsertTimeout)
	defer cancel()
	b, err := s.conn.PrepareBatch(ctx, s.insertQuery(t))
	if err != nil {
		s.drop(DropInsert, len(rows))
		s.logErr("clickhouse insert failed", tableDefs[t].name, len(rows), err)
		return
	}
	appended := 0
	for _, r := range rows {
		if err := b.Append(r...); err != nil {
			s.drop(DropInsert, 1)
			s.logErr("clickhouse row rejected", tableDefs[t].name, 1, err)
			continue
		}
		appended++
	}
	if appended == 0 {
		_ = b.Abort()
		return
	}
	if err := b.Send(); err != nil {
		s.drop(DropInsert, appended)
		s.logErr("clickhouse insert failed", tableDefs[t].name, appended, err)
	}
}

// logErr logs at most one insert error per minute.
func (s *ClickHouse) logErr(msg, tbl string, rows int, err error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if time.Since(s.lastLog) < time.Minute {
		return
	}
	s.lastLog = time.Now()
	s.o.Log.Warn(msg, "table", tbl, "rows", rows, "err", err)
}

// Flush inserts every queued row now.
func (s *ClickHouse) Flush(ctx context.Context) error {
	ack := make(chan struct{})
	select {
	case s.flushReq <- ack:
	case <-s.done:
		return errors.New("telemetry: sink closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting rows, inserts what is queued (at most FlushTimeout) and closes the
// connection.
func (s *ClickHouse) Close() error {
	s.once.Do(func() {
		s.closing.Store(true)
		close(s.stop)
		t := time.AfterFunc(FlushTimeout, s.cancelBase)
		<-s.done
		if !t.Stop() {
			s.closeErr = errors.New("telemetry: final flush timed out")
		}
		s.cancelBase()
		s.closeErr = errors.Join(s.closeErr, s.conn.Close())
	})
	return s.closeErr
}

// ErasePlayer inserts the queued rows, then deletes the player's rows from every table.
func (s *ClickHouse) ErasePlayer(ctx context.Context, tenantID, player string) error {
	if !identity.ValidTenantID(tenantID) {
		return errors.New("telemetry: invalid tenant id")
	}
	if _, err := identity.ParseCanonicalUUID(player); err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	if err := s.Flush(ctx); err != nil {
		return err
	}
	var errs []error
	for t := range numTables {
		q := "ALTER TABLE " + s.db + "." + tableDefs[t].name + " DELETE WHERE tenant_id = ? AND player = toUUID(?)"
		if err := s.conn.Exec(ctx, q, tenantID, player); err != nil {
			errs = append(errs, fmt.Errorf("telemetry: erase %s: %w", tableDefs[t].name, err))
		}
	}
	return errors.Join(errs...)
}

// RecordSignal queues a signals row (probe signals are skipped).
func (s *ClickHouse) RecordSignal(sig *pb.Signal) {
	if sig == nil || sig.CheckId == checks.ProbeCheckID {
		return
	}
	o := sig.GetOrigin()
	s.enqueue(tableSignals, []any{
		o.GetTenantId(), normTime(sig.EventTimeUs, o.GetClockOffsetUs(), sig.GatewayRecvUnixUs), playerUUID(sig.Player), sig.PlayerName,
		parseUUID(o.GetServerId()), o.GetServerName(), o.GetNetworkId(), parseUUID(sig.SessionId), sig.CheckId, sig.Category.String(),
		sig.Points, sig.Confidence, sig.EventTimeUs, sig.GatewayRecvUnixUs, sig.WorkerUnixUs, sig.Info, cloneData(sig.Data),
		sig.ClipId, sig.Local, sig.Replayed, sig.HotLoaded, o.GetShadowMode(), o.GetPlatform().String(),
	})
}

// RecordVerdict queues a verdicts row.
func (s *ClickHouse) RecordVerdict(r *pb.VerdictRecord) {
	if r == nil || r.Verdict == nil {
		return
	}
	v := r.Verdict
	mitigations := make([]string, len(v.Mitigations))
	for i, m := range v.Mitigations {
		mitigations[i] = m.String()
	}
	s.enqueue(tableVerdicts, []any{
		r.TenantId, unixMicro(int64(r.CreatedUnixUs)), parseUUID(v.VerdictId), playerUUID(v.Player), v.PlayerName,
		parseUUID(r.ServerId), r.NetworkId, parseUUID(r.SessionId), v.Action.String(), v.Category.String(), v.Score, v.Confidence,
		append([]string{}, v.Checks...), mitigations, v.Strength, v.Shadow, v.BanDurationS, v.Supersedes, v.Reason,
		append([]string{}, r.ClipIds...), r.ReputationEligible,
	})
}

// RecordSession queues a sessions row; join (may be nil) fills the client columns.
func (s *ClickHouse) RecordSession(m *pb.SessionMarker, join *pb.PlayerJoin) {
	if m == nil {
		return
	}
	o := m.GetOrigin()
	protocol, version, brand, bedrock := int32(-1), "", "", false
	if join != nil {
		protocol, version, bedrock = join.ProtocolVersion, join.ClientVersion, join.Bedrock
		brand = truncate(join.Brand, maxBrandLen)
	}
	at := int64(m.GatewayRecvUnixUs)
	if at == 0 {
		at = time.Now().UnixMicro()
	}
	s.enqueue(tableSessions, []any{
		o.GetTenantId(), unixMicro(at), playerUUID(m.Player), m.PlayerName, m.Kind.String(), parseUUID(m.SessionId),
		m.SessionStartUs, parseUUID(o.GetServerId()), o.GetServerName(), o.GetNetworkId(), m.NetworkSession,
		o.GetPlatform().String(), m.HotLoaded, m.Exempt, o.GetShadowMode(), protocol, version, brand, bedrock,
	})
}

// RecordFeatureWindow queues a feature_windows row.
func (s *ClickHouse) RecordFeatureWindow(w FeatureWindow) {
	s.enqueue(tableFeatures, []any{
		w.TenantID, uuid.UUID(w.Player), parseUUID(w.ServerID), parseUUID(w.SessionID), w.WindowStartUs, w.WindowEndUs,
		w.ClientProtocol, boolByte(w.TickClient), boolByte(w.HotLoaded), w.FeatureVersion, append([]float32{}, w.Features...),
		w.WorkerUnixUs,
	})
}

// normTime is the normalised event time (event_time_us + clock_offset_us); without an event time,
// the cloud receive time.
func normTime(eventUs uint64, offsetUs int64, fallbackUs uint64) time.Time {
	if eventUs == 0 {
		return unixMicro(int64(fallbackUs))
	}
	return unixMicro(int64(eventUs) + offsetUs)
}

func unixMicro(us int64) time.Time {
	if us < 0 {
		us = 0
	}
	return time.UnixMicro(us).UTC()
}

// parseUUID converts a canonical UUID string; anything else is the nil UUID (init.sql convention).
func parseUUID(s string) uuid.UUID {
	u, err := identity.ParseCanonicalUUID(s)
	if err != nil {
		return uuid.Nil
	}
	return uuid.UUID(u)
}

func playerUUID(b []byte) uuid.UUID {
	u, err := identity.UUIDFromBytes(b)
	if err != nil {
		return uuid.Nil
	}
	return uuid.UUID(u)
}

func cloneData(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return maps.Clone(m)
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
