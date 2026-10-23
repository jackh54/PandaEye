package telemetry

import (
	"context"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// SignalSink receives signals (table signals).
type SignalSink interface {
	// RecordSignal queues one row for s; probe signals are ignored.
	RecordSignal(s *pb.Signal)
}

// VerdictSink receives verdict records (table verdicts).
type VerdictSink interface {
	// RecordVerdict queues one row for r (appeal code and evidence are not stored).
	RecordVerdict(r *pb.VerdictRecord)
}

// SessionSink receives session markers (table sessions).
type SessionSink interface {
	// RecordSession queues one row for m; join is the session's PlayerJoin when the caller holds it
	// (client protocol, version, brand, bedrock), else nil.
	RecordSession(m *pb.SessionMarker, join *pb.PlayerJoin)
}

// FeatureSink receives ML feature windows (table feature_windows, ml/FEATURES.md).
type FeatureSink interface {
	// RecordFeatureWindow queues one window row.
	RecordFeatureWindow(w FeatureWindow)
}

// ErasureSink deletes a player's telemetry (ARCHITECTURE 5.13).
type ErasureSink interface {
	// ErasePlayer deletes every row of (tenantID, player) from every table; player is a canonical
	// UUID. Queued rows are inserted first, so none of them survives the erasure.
	ErasePlayer(ctx context.Context, tenantID, player string) error
}

// Sink is the complete telemetry sink.
type Sink interface {
	SignalSink
	VerdictSink
	SessionSink
	FeatureSink
	ErasureSink
	// Dropped returns how many rows were dropped (queue overflow or failed inserts).
	Dropped() uint64
	// Close flushes pending rows (bounded by FlushTimeout) and releases the connection.
	Close() error
}

// FeatureWindow is one row of feature_windows: the metadata columns of ml/FEATURES.md section 5 and
// the feature vector in table order. time and inserted_at take their column defaults.
type FeatureWindow struct {
	TenantID       string
	Player         identity.UUID
	ServerID       string // canonical UUID of the session's server
	SessionID      string // PlayerJoin.session (canonical UUID; anything else stores the nil UUID)
	WindowStartUs  int64  // normalised Unix microseconds, a multiple of 30 000 000
	WindowEndUs    int64  // WindowStartUs + 30 000 000
	ClientProtocol int32  // PlayerJoin.protocol_version, -1 unknown
	TickClient     bool   // ClientProtocol >= 768
	HotLoaded      bool
	FeatureVersion uint16 // 1
	Features       []float32
	WorkerUnixUs   int64 // ReplacingMergeTree version: the newest emission wins
}

// New returns the process's sink, shared by every service of the process (app.Deps.Shared): the
// feature-window files when PE_FEATURES_DIR is set (`all` only), the ClickHouse sink when
// PE_CLICKHOUSE_DSN is set, else Nop. The sink is closed by d.Close.
func New(d *app.Deps) (Sink, error) {
	s, err := d.Shared("telemetry", func() (any, error) {
		if d.Config != nil && d.Config.Features.Dir != "" {
			f, err := NewFiles(d.Config.Features.Dir, FileOptions{Log: d.Log, Metrics: d.Metrics,
				Retention: d.Config.Features.Retention, MaxBytes: d.Config.Features.MaxBytes})
			if err != nil {
				return nil, err
			}
			d.OnClose(f.Close)
			return Sink(f), nil
		}
		if d.Config == nil || d.Config.ClickHouseDSN.IsZero() {
			return Nop{}, nil
		}
		ch, err := NewClickHouse(d.Config.ClickHouseDSN.Reveal(), Options{Log: d.Log, Metrics: d.Metrics})
		if err != nil {
			return nil, err
		}
		d.OnClose(ch.Close)
		return Sink(ch), nil
	})
	if err != nil {
		return nil, err
	}
	return s.(Sink), nil
}

// Nop is the disabled sink: it accepts and discards everything.
type Nop struct{}

var _ Sink = Nop{}

func (Nop) RecordSignal(*pb.Signal)                           {}
func (Nop) RecordVerdict(*pb.VerdictRecord)                   {}
func (Nop) RecordSession(*pb.SessionMarker, *pb.PlayerJoin)   {}
func (Nop) RecordFeatureWindow(FeatureWindow)                 {}
func (Nop) ErasePlayer(context.Context, string, string) error { return nil }
func (Nop) Dropped() uint64                                   { return 0 }
func (Nop) Close() error                                      { return nil }
