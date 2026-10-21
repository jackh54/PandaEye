package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	dto "github.com/prometheus/client_model/go"
)

// fakeConn records batches and statements instead of talking to ClickHouse.
type fakeConn struct {
	mu         sync.Mutex
	sent       []*fakeBatch
	execs      []string
	execArgs   [][]any
	prepareErr error
	block      chan struct{} // PrepareBatch waits on it when set
	closed     bool
}

type fakeBatch struct {
	c     *fakeConn
	query string
	rows  [][]any
}

func (f *fakeConn) PrepareBatch(ctx context.Context, q string) (batch, error) {
	f.mu.Lock()
	block, err := f.block, f.prepareErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &fakeBatch{c: f, query: q}, nil
}

func (f *fakeConn) Exec(_ context.Context, q string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, q)
	f.execArgs = append(f.execArgs, args)
	return nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (b *fakeBatch) Append(v ...any) error {
	b.rows = append(b.rows, v)
	return nil
}

func (b *fakeBatch) Send() error {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	b.c.sent = append(b.c.sent, b)
	return nil
}

func (b *fakeBatch) Abort() error { return nil }

func (f *fakeConn) batches() []*fakeBatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeBatch(nil), f.sent...)
}

func (f *fakeConn) rows(table string) [][]any {
	var out [][]any
	for _, b := range f.batches() {
		if strings.HasPrefix(b.query, "INSERT INTO pandaeye."+table+" (") {
			out = append(out, b.rows...)
		}
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newFake(t *testing.T, o Options) (*ClickHouse, *fakeConn) {
	t.Helper()
	f := &fakeConn{}
	s, err := newClickHouse(f, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, f
}

const (
	tenant = "t_abcdefghijklmnopqrst"
	server = "5f0c6b1e-8d3a-4c2b-9e7f-1a2b3c4d5e6f"
)

var player = identity.UUID(uuid.MustParse("0b7e2c4a-1111-4222-8333-944455556666"))

func signal(check string) *pb.Signal {
	return &pb.Signal{
		Origin: &pb.Origin{TenantId: tenant, ServerId: server, ServerName: "Hub", NetworkId: "net", ShadowMode: true,
			Platform: pb.Platform_PLATFORM_FOLIA, ClockOffsetUs: 2_000_000},
		Player: player.Bytes(), PlayerName: "Steve", CheckId: check, Category: pb.Category_CATEGORY_HEURISTICS, Points: 5,
		Confidence: 0.8, EventTimeUs: 1_700_000_000_000_000, GatewayRecvUnixUs: 1_700_000_002_100_000, WorkerUnixUs: 1_700_000_002_200_000,
		Info: "miss", Data: map[string]float64{"angle": 12.5}, ClipId: "clip", Local: false, Replayed: true, HotLoaded: true,
		SessionId: "not-a-uuid",
	}
}

func TestColumnsMatchInitSQL(t *testing.T) {
	// Every insert lists exactly the columns of its table definition.
	for tb, d := range tableDefs {
		s := &ClickHouse{db: "pandaeye"}
		q := s.insertQuery(table(tb))
		if !strings.HasPrefix(q, "INSERT INTO pandaeye."+d.name+" (tenant_id, ") || strings.Count(q, ",")+1 != len(d.cols) {
			t.Fatalf("%s: %s", d.name, q)
		}
	}
}

func TestSignalRows(t *testing.T) {
	s, f := newFake(t, Options{FlushInterval: 20 * time.Millisecond})
	sig := signal("aim.snap")
	s.RecordSignal(sig)
	s.RecordSignal(signal("probe")) // probes are not stored
	sig.Data["angle"] = 99          // rows are copied when recorded
	eventually(t, "signal row", func() bool { return len(f.rows("signals")) == 1 })
	r := f.rows("signals")[0]
	if len(r) != len(tableDefs[tableSignals].cols) {
		t.Fatalf("%d values for %d columns", len(r), len(tableDefs[tableSignals].cols))
	}
	want := map[string]any{
		"tenant_id": tenant, "time": time.UnixMicro(1_700_000_002_000_000).UTC(), "player": uuid.UUID(player),
		"server_id": uuid.MustParse(server), "session_id": uuid.Nil, "check_id": "aim.snap", "category": "CATEGORY_HEURISTICS",
		"points": 5.0, "event_time_us": uint64(1_700_000_000_000_000), "local": false, "replayed": true, "hot_loaded": true,
		"shadow_mode": true, "platform": "PLATFORM_FOLIA", "network_id": "net", "server_name": "Hub",
	}
	for i, c := range tableDefs[tableSignals].cols {
		if w, ok := want[c]; ok {
			if tm, isTime := w.(time.Time); isTime {
				if !r[i].(time.Time).Equal(tm) {
					t.Errorf("%s = %v, want %v", c, r[i], w)
				}
			} else if r[i] != w {
				t.Errorf("%s = %#v, want %#v", c, r[i], w)
			}
		}
		if c == "data" && r[i].(map[string]float64)["angle"] != 12.5 {
			t.Error("data not copied")
		}
	}
}

func TestVerdictSessionFeatureRows(t *testing.T) {
	s, f := newFake(t, Options{FlushInterval: 20 * time.Millisecond})
	vid := identity.NewUUID().String()
	s.RecordVerdict(&pb.VerdictRecord{TenantId: tenant, ServerId: server, NetworkId: "net", SessionId: vid, CreatedUnixUs: 1_700_000_000_000_000,
		ClipIds: []string{"c1"}, ReputationEligible: true,
		Verdict: &pb.Verdict{VerdictId: vid, Player: player.Bytes(), PlayerName: "Steve", Action: pb.Action_ACTION_BAN,
			Mitigations: []pb.Mitigation{pb.Mitigation_MITIGATION_SETBACK}, Checks: []string{"aim.snap"}, Score: 200, AppealCode: "SECRET"}})
	s.RecordSession(&pb.SessionMarker{Origin: &pb.Origin{TenantId: tenant, ServerId: server}, Player: player.Bytes(),
		Kind: pb.SessionMarker_KIND_JOIN, SessionId: vid, GatewayRecvUnixUs: 1_700_000_000_000_000},
		&pb.PlayerJoin{ProtocolVersion: 769, ClientVersion: "1.21.4", Brand: strings.Repeat("b", 100), Bedrock: true})
	s.RecordSession(&pb.SessionMarker{Origin: &pb.Origin{TenantId: tenant}, Player: player.Bytes(), Kind: pb.SessionMarker_KIND_QUIT}, nil)
	s.RecordFeatureWindow(FeatureWindow{TenantID: tenant, Player: player, ServerID: server, SessionID: vid, WindowStartUs: 30_000_000,
		WindowEndUs: 60_000_000, ClientProtocol: 769, TickClient: true, FeatureVersion: 1, Features: []float32{1, 2, 3}, WorkerUnixUs: 5})
	eventually(t, "rows", func() bool {
		return len(f.rows("verdicts")) == 1 && len(f.rows("sessions")) == 2 && len(f.rows("feature_windows")) == 1
	})
	v := f.rows("verdicts")[0]
	if len(v) != len(tableDefs[tableVerdicts].cols) || v[8] != "ACTION_BAN" || v[13].([]string)[0] != "MITIGATION_SETBACK" || v[20] != true {
		t.Fatalf("verdict row %v", v)
	}
	for _, val := range v {
		if val == "SECRET" {
			t.Fatal("appeal codes are never stored in telemetry")
		}
	}
	join, quit := f.rows("sessions")[0], f.rows("sessions")[1]
	if join[4] != "KIND_JOIN" || join[15] != int32(769) || len(join[17].(string)) != 64 || join[18] != true {
		t.Fatalf("join row %v", join)
	}
	if quit[15] != int32(-1) || quit[16] != "" || quit[5] != uuid.Nil {
		t.Fatalf("quit row defaults %v", quit)
	}
	fw := f.rows("feature_windows")[0]
	if len(fw) != len(tableDefs[tableFeatures].cols) || fw[7] != uint8(1) || fw[8] != uint8(0) || fw[9] != uint16(1) || len(fw[10].([]float32)) != 3 {
		t.Fatalf("feature row %v", fw)
	}
}

func TestBatchingByRowsAndInterval(t *testing.T) {
	s, f := newFake(t, Options{BatchRows: 3, FlushInterval: time.Hour})
	for range 7 {
		s.RecordSignal(signal("aim.snap"))
	}
	eventually(t, "two full batches", func() bool { return len(f.batches()) == 2 })
	if n := len(f.batches()[0].rows); n != 3 {
		t.Fatalf("batch of %d rows", n)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := f.batches(); len(b) != 3 || len(b[2].rows) != 1 {
		t.Fatalf("flush: %d batches", len(b))
	}

	// The interval flushes partial batches.
	s2, f2 := newFake(t, Options{FlushInterval: 30 * time.Millisecond})
	s2.RecordSignal(signal("aim.snap"))
	s2.RecordVerdict(&pb.VerdictRecord{TenantId: tenant, Verdict: &pb.Verdict{}})
	eventually(t, "interval flush", func() bool { return len(f2.batches()) == 2 })
}

func TestOverflowDropsAndCounts(t *testing.T) {
	m := metrics.New()
	f := &fakeConn{block: make(chan struct{})}
	s, err := newClickHouse(f, Options{QueueSize: 2, BatchRows: 1, FlushInterval: time.Hour, Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		s.RecordSignal(signal("aim.snap")) // the loop holds 1 row in a blocked insert, the queue 2
	}
	if d := s.Dropped(); d < 7 {
		t.Fatalf("dropped %d", d)
	}
	var mv dto.Metric
	if err := m.Drops.WithLabelValues(DropOverflow).Write(&mv); err != nil || mv.GetCounter().GetValue() != float64(s.Dropped()) {
		t.Fatalf("metric %v vs %d (%v)", mv.GetCounter().GetValue(), s.Dropped(), err)
	}
	close(f.block)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !f.closed {
		t.Fatal("connection not closed")
	}
	s.RecordSignal(signal("aim.snap")) // after Close: dropped, no panic
}

func TestFailedInsertDropsBatch(t *testing.T) {
	s, f := newFake(t, Options{FlushInterval: time.Hour})
	f.prepareErr = errors.New("clickhouse down")
	s.RecordSignal(signal("aim.snap"))
	s.RecordSignal(signal("aim.snap"))
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Dropped() != 2 || len(f.batches()) != 0 {
		t.Fatalf("dropped %d", s.Dropped())
	}
}

func TestErasePlayerFlushesFirst(t *testing.T) {
	s, f := newFake(t, Options{FlushInterval: time.Hour})
	s.RecordSignal(signal("aim.snap"))
	if err := s.ErasePlayer(context.Background(), tenant, player.String()); err != nil {
		t.Fatal(err)
	}
	if len(f.batches()) != 1 {
		t.Fatal("queued rows must be inserted before the delete")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execs) != 4 {
		t.Fatalf("statements %v", f.execs)
	}
	for i, tb := range []string{"signals", "verdicts", "sessions", "feature_windows"} {
		if f.execs[i] != "ALTER TABLE pandaeye."+tb+" DELETE WHERE tenant_id = ? AND player = toUUID(?)" ||
			f.execArgs[i][0] != tenant || f.execArgs[i][1] != player.String() {
			t.Fatalf("%s: %q %v", tb, f.execs[i], f.execArgs[i])
		}
	}
	if s.ErasePlayer(context.Background(), "bad tenant", player.String()) == nil ||
		s.ErasePlayer(context.Background(), tenant, "'; DROP TABLE x") == nil {
		t.Fatal("invalid ids accepted")
	}
}

func TestCloseFlushesQueue(t *testing.T) {
	f := &fakeConn{}
	s, err := newClickHouse(f, Options{FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s.RecordSignal(signal("aim.snap"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(f.rows("signals")) != 1 {
		t.Fatal("Close lost queued rows")
	}
}

func TestDatabaseName(t *testing.T) {
	if _, err := newClickHouse(&fakeConn{}, Options{Database: "pe; DROP"}); err == nil {
		t.Fatal("unsafe database name accepted")
	}
	s, _ := newFake(t, Options{Database: "custom_db"})
	if !strings.HasPrefix(s.insertQuery(tableSignals), "INSERT INTO custom_db.signals ") {
		t.Fatal(s.insertQuery(tableSignals))
	}
	if _, err := NewClickHouse("::not a dsn::", Options{}); err == nil || strings.Contains(err.Error(), "not a dsn") {
		t.Fatalf("bad DSN error must not echo the DSN: %v", err)
	}
}

func TestNewIsNopWithoutDSN(t *testing.T) {
	d := testkit.NewDeps(t, "PE_EVENTS_MAX_BYTES=256MiB")
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(Nop); !ok {
		t.Fatalf("sink %T", s)
	}
	again, _ := New(d)
	if again != s {
		t.Fatal("the sink must be shared per process")
	}
}
