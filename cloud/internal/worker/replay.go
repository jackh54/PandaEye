package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/ml"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/telemetry"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// ReplayOptions selects what `pandaeye replay` re-checks.
type ReplayOptions struct {
	TenantID string
	Player   string // canonical UUID
	Since    time.Time
	Until    time.Time
	ClipID   string // replay a stored clip instead of stream data
}

// ReplayLine is one printed signal (a JSON line).
type ReplayLine struct {
	Time        string             `json:"time"` // normalised event time, RFC 3339
	Check       string             `json:"check"`
	Points      float64            `json:"points"`
	Confidence  float64            `json:"confidence"`
	Local       bool               `json:"local,omitempty"`
	Player      string             `json:"player"`
	PlayerName  string             `json:"player_name,omitempty"`
	ServerID    string             `json:"server_id"`
	SessionID   string             `json:"session_id,omitempty"`
	EventTimeUs uint64             `json:"event_time_us"`
	Info        string             `json:"info,omitempty"`
	Data        map[string]float64 `json:"data,omitempty"`
}

var errReplayDone = errors.New("replay: past --until")

// Replay re-runs the checks over a stored clip (o.ClipID) or over PE_EVENTS for a player and time
// range, and writes every signal of that player to out as JSON lines. Stream data from the 60 s
// before Since rebuilds state silently first (as a worker warm start does). Nothing is published.
func Replay(ctx context.Context, d *app.Deps, o ReplayOptions, out io.Writer) error {
	if !identity.ValidTenantID(o.TenantID) {
		return fmt.Errorf("replay: invalid tenant id %q", o.TenantID)
	}
	en := replayEnv(ctx, d)
	enc := json.NewEncoder(out)
	print := func(player identity.UUID, msgs []outMsg) error {
		for _, m := range msgs {
			sig := m.env.GetSignal()
			if sig == nil || sig.GetCheckId() == checks.ProbeCheckID {
				continue
			}
			if p, err := identity.UUIDFromBytes(sig.GetPlayer()); err != nil || p != player {
				continue
			}
			t := time.UnixMicro(int64(sig.GetEventTimeUs()) + sig.GetOrigin().GetClockOffsetUs()).UTC()
			if err := enc.Encode(ReplayLine{Time: t.Format(time.RFC3339Nano), Check: sig.GetCheckId(), Points: sig.GetPoints(),
				Confidence: sig.GetConfidence(), Local: sig.GetLocal(), Player: player.String(), PlayerName: sig.GetPlayerName(),
				ServerID: sig.GetOrigin().GetServerId(), SessionID: sig.GetSessionId(), EventTimeUs: sig.GetEventTimeUs(),
				Info: sig.GetInfo(), Data: sig.GetData()}); err != nil {
				return err
			}
		}
		return nil
	}
	if o.ClipID != "" {
		return replayClip(ctx, d, en, o, print)
	}
	if d.Bus == nil {
		return errors.New("replay: stream data needs the event bus (set PE_NATS_URL), or use --clip")
	}
	player, err := identity.ParseUUID(o.Player)
	if err != nil {
		return fmt.Errorf("replay: --player: %w", err)
	}
	part := d.Bus.Partitioner.ForUUID(player)
	st, err := d.Bus.JS.Stream(ctx, stream.StreamEvents)
	if err != nil {
		return err
	}
	info, err := st.Info(ctx)
	if err != nil {
		return err
	}
	eng := newEngine(part, en, nil)
	_, err = d.Bus.Replay(ctx, stream.ReplaySpec{Stream: stream.StreamEvents, Subject: stream.EventsSubject(part),
		Since: o.Since.Add(-warmStartSpan), UpTo: info.State.LastSeq}, func(m *stream.Msg) error {
		if m.Time.After(o.Until) {
			return errReplayDone
		}
		sb, raws, err := decodeBatch(m.Data)
		if err != nil || sb.GetOrigin().GetTenantId() != o.TenantID {
			return nil
		}
		filterPlayer(sb, &raws, player)
		return print(player, eng.handle(m.Seq, sb, raws, m.Time.Before(o.Since)))
	})
	if errors.Is(err, errReplayDone) {
		return nil
	}
	return err
}

// filterPlayer keeps the player's events and the server stalls of a batch.
func filterPlayer(sb *pb.StreamBatch, raws *[][]byte, player identity.UUID) {
	keepRaw := *raws != nil
	evs := sb.Events[:0]
	var rs [][]byte
	for i, ev := range sb.GetEvents() {
		p, err := identity.UUIDFromBytes(ev.GetPlayer())
		if (err == nil && p == player) || ev.GetStall() != nil {
			evs = append(evs, ev)
			if keepRaw {
				rs = append(rs, (*raws)[i])
			}
		}
	}
	sb.Events = evs
	*raws = rs
}

func replayClip(ctx context.Context, d *app.Deps, en *env, o ReplayOptions, print func(identity.UUID, []outMsg) error) error {
	c, err := d.Store.GetClip(ctx, o.TenantID, o.ClipID)
	if err != nil {
		return fmt.Errorf("replay: clip %s: %w", o.ClipID, err)
	}
	dec, err := codec.NewDecoder(codec.Limits{})
	if err != nil {
		return err
	}
	defer dec.Close()
	raw, err := dec.DecodeZstd(ctx, c.Data, codec.MaxDecompressed)
	if err != nil {
		return fmt.Errorf("replay: clip %s: %w", o.ClipID, err)
	}
	var cd pb.ClipData
	if err := proto.Unmarshal(raw, &cd); err != nil {
		return fmt.Errorf("replay: clip %s: %w", o.ClipID, err)
	}
	player, err := identity.UUIDFromBytes(cd.GetPlayer())
	if err != nil {
		return fmt.Errorf("replay: clip %s has no player", o.ClipID)
	}
	origin := cd.GetOrigin()
	if origin == nil || origin.GetTenantId() != o.TenantID {
		return fmt.Errorf("replay: clip %s belongs to another tenant", o.ClipID)
	}
	events := cd.GetEvents()
	hasJoin := false
	for _, ev := range events {
		hasJoin = hasJoin || ev.GetJoin() != nil
	}
	if !hasJoin && len(events) > 0 {
		// Clips carry the session's PlayerJoin; older ones may not: recover the session as a worker
		// does after a restart, with an unknown client version.
		join := &pb.Event{Player: cd.GetPlayer(), TimeUs: events[0].GetTimeUs(),
			Kind: &pb.Event_Join{Join: &pb.PlayerJoin{Update: true, ProtocolVersion: -1}}}
		events = append([]*pb.Event{join}, events...)
	}
	eng := newEngine(0, en, nil)
	msgs := eng.handle(0, &pb.StreamBatch{Origin: origin, Events: events}, nil, false)
	quit := &pb.Event{Player: cd.GetPlayer(), TimeUs: cd.GetEndUs() + 1, Kind: &pb.Event_Quit{Quit: &pb.PlayerQuit{Reason: "end of clip"}}}
	msgs = append(msgs, eng.handle(0, &pb.StreamBatch{Origin: origin, Events: []*pb.Event{quit}}, nil, false)...)
	return print(player, msgs)
}

// replayEnv is an engine environment that saves nothing: no clips, no telemetry, no model.
func replayEnv(ctx context.Context, d *app.Deps) *env {
	m := d.Metrics
	if m == nil {
		m = metrics.New()
	}
	log := d.Log
	en := &env{log: log, m: m, wm: newWorkerMetrics(m), factories: checks.Factories(), tenants: noTenants{},
		erasures: noErasures{}, budget: newBudget(0), clips: newClipPolicy(), sessions: telemetry.Nop{},
		windows: telemetry.Nop{}, scorer: ml.Disabled{}, now: time.Now}
	if d.Bus != nil {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if tw, err := d.Bus.WatchTenants(ctx); err == nil {
			select {
			case <-tw.Ready():
				en.tenants = tw
			case <-wctx.Done():
			}
		}
		if ew, err := d.Bus.WatchErasures(ctx); err == nil {
			select {
			case <-ew.Ready():
				en.erasures = ew
			case <-wctx.Done():
			}
		}
	}
	return en
}
