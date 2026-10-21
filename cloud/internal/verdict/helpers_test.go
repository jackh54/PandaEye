package verdict

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

var testOutKey = []byte("0123456789abcdef0123456789abcdef-test-outbound-key")

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// harness drives a partition directly (no NATS): messages are built here and applied in order.
type harness struct {
	t        *testing.T
	clk      *clock
	st       store.Store
	settings map[string]stream.TenantSettings
	erased   map[playerKey]time.Time
	p        *partition
	seq      uint64
	idPrefix string
	sink     *recordingSink
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, clk: &clock{t: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}, st: testkit.NewStore(t),
		settings: map[string]stream.TenantSettings{}, erased: map[playerKey]time.Time{}, sink: &recordingSink{},
		idPrefix: identity.NewUUID().String()[:8]}
	e := &env{
		now: h.clk.now,
		tenant: func(id string) stream.TenantSettings {
			if ts, ok := h.settings[id]; ok {
				return ts
			}
			return defaultSettings(id)
		},
		erasedAt: func(tenant string, p identity.UUID) (time.Time, bool) {
			at, ok := h.erased[playerKey{tenant, p}]
			return at, ok
		},
		store: h.st, signals: h.sink, probes: newProbeStats(), outKey: testOutKey, publicURL: "https://dash.example",
		metrics: metrics.New(), log: slog.New(slog.DiscardHandler),
	}
	h.p = newPartition(e, 0)
	return h
}

// tenant registers a tenant (store row + settings) with enforcement and every auto-action on.
func (h *harness) tenant(mod ...func(*stream.TenantSettings)) string {
	h.t.Helper()
	id := identity.NewTenantID()
	tn := store.NewTenant(id, "t", id, h.clk.now())
	if err := h.st.CreateTenant(context.Background(), tn); err != nil {
		h.t.Fatal(err)
	}
	ts := stream.TenantSettings{TenantID: id, Leniency: 4, Autoban: true, Autokick: true, Mitigations: true, Enforcement: true,
		DiscordMinAction: pb.Action_ACTION_MITIGATE, Plan: "pro"}
	for _, m := range mod {
		m(&ts)
	}
	h.settings[id] = ts
	return id
}

// tplayer is a test player on one server.
type tplayer struct {
	clock   func() time.Time
	tenant  string
	id      identity.UUID
	name    string
	server  string
	network string
	session string
	offset  int64 // clock offset (us)
	shadow  bool
	hot     bool
	exempt  bool
}

func (h *harness) player(tenant string) *tplayer {
	return &tplayer{clock: h.clk.now, tenant: tenant, id: identity.NewUUID(), name: "Steve", server: testkit.NewServerID(),
		network: "net-1", session: identity.NewUUID().String()}
}

func (tp *tplayer) origin() *pb.Origin {
	return &pb.Origin{TenantId: tp.tenant, ServerId: tp.server, ServerName: "srv-" + tp.server[:4], NetworkId: tp.network,
		ShadowMode: tp.shadow, Platform: pb.Platform_PLATFORM_PAPER, ClockOffsetUs: tp.offset}
}

// nowUs is the harness clock in plugin time (normalised = plugin + offset).
func (tp *tplayer) pluginNow() uint64 { return uint64(tp.clock().UnixMicro() - tp.offset) }

func (tp *tplayer) marker(kind pb.SessionMarker_Kind) *pb.SignalEnvelope {
	return &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Session{Session: &pb.SessionMarker{Origin: tp.origin(), Player: tp.id.Bytes(),
		PlayerName: tp.name, Kind: kind, SessionId: tp.session, SessionStartUs: tp.pluginNow(), HotLoaded: tp.hot, Exempt: tp.exempt,
		GatewayRecvUnixUs: uint64(tp.clock().UnixMicro())}}}
}

func (tp *tplayer) signal(check string, points float64, mod ...func(*pb.Signal)) *pb.SignalEnvelope {
	now := tp.clock()
	s := &pb.Signal{Origin: tp.origin(), Player: tp.id.Bytes(), PlayerName: tp.name, CheckId: check, Points: points,
		EventTimeUs: tp.pluginNow(), GatewayRecvUnixUs: uint64(now.UnixMicro()), WorkerUnixUs: uint64(now.UnixMicro()),
		SessionId: tp.session, HotLoaded: tp.hot, Info: "test", Data: map[string]float64{"x": 1}}
	for _, m := range mod {
		m(s)
	}
	return &pb.SignalEnvelope{Kind: &pb.SignalEnvelope_Signal{Signal: s}}
}

func withClip(id string) func(*pb.Signal) { return func(s *pb.Signal) { s.ClipId = id } }
func withConf(c float64) func(*pb.Signal) { return func(s *pb.Signal) { s.Confidence = c } }
func local() func(*pb.Signal)             { return func(s *pb.Signal) { s.Local = true } }
func withRecvAgo(d time.Duration) func(*pb.Signal) {
	return func(s *pb.Signal) { s.GatewayRecvUnixUs -= uint64(d.Microseconds()) }
}
func withEventAgo(d time.Duration) func(*pb.Signal) {
	return func(s *pb.Signal) { s.EventTimeUs -= uint64(d.Microseconds()) }
}

// msg wraps an envelope as the stream message with the next sequence and a unique msg id (or the
// given one).
func (h *harness) msg(e *pb.SignalEnvelope, id ...string) *stream.Msg {
	h.t.Helper()
	data, err := proto.Marshal(e)
	if err != nil {
		h.t.Fatal(err)
	}
	h.seq++
	mid := "m-" + h.idPrefix + "-" + strconv.FormatUint(h.seq, 10)
	if len(id) > 0 {
		mid = id[0]
	}
	return &stream.Msg{Seq: h.seq, Subject: stream.SignalsSubject(0), Data: data, Header: nats.Header{nats.MsgIdHdr: []string{mid}}}
}

// apply applies an envelope live and returns its effects.
func (h *harness) apply(e *pb.SignalEnvelope) effects {
	h.t.Helper()
	return h.p.apply(h.msg(e), true)
}

// actions returns the actions of the effects' verdicts.
func actions(fx effects) []pb.Action {
	var out []pb.Action
	for _, v := range fx.verdicts {
		out = append(out, v.rec.Verdict.Action)
	}
	return out
}

func (h *harness) score(tp *tplayer) Score {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	pl := h.p.players[playerKey{tp.tenant, tp.id}]
	if pl == nil {
		return Score{}
	}
	return pl.score.snapshot()
}

// recordingSink records telemetry calls.
type recordingSink struct {
	telemetry.Nop
	mu       sync.Mutex
	signals  []*pb.Signal
	verdicts []*pb.VerdictRecord
}

func (r *recordingSink) RecordSignal(s *pb.Signal) {
	r.mu.Lock()
	r.signals = append(r.signals, s)
	r.mu.Unlock()
}

func (r *recordingSink) RecordVerdict(v *pb.VerdictRecord) {
	r.mu.Lock()
	r.verdicts = append(r.verdicts, v)
	r.mu.Unlock()
}

func (r *recordingSink) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.signals), len(r.verdicts)
}

func near(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }
