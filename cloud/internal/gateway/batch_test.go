package gateway

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestBatchStoredAckedAndDeduplicated(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	c, w := e.connect(s, func(h *pb.Hello) { h.ServerName, h.NetworkId = "Hub-01", "net" })
	var players []*testkit.Player
	var events []*pb.Event
	for i := 0; i < 12; i++ {
		p := testkit.NewPlayer(fmt.Sprintf("P%d", i))
		players = append(players, p)
		events = append(events, p.Join(), p.Move(geom.V(0.1, 0, 0), geom.Rotation{}))
	}
	b := testkit.Batch(1, events...)
	before := time.Now()
	sendBatch(t, c, b)

	stored := testkit.EventBatches(t, e.d.Bus)
	parts := map[int]bool{}
	for _, p := range players {
		n, _ := e.d.Bus.Partitioner.ForPlayer(p.UUID)
		parts[n] = true
	}
	if len(stored) != len(parts) {
		t.Fatalf("%d stream batches for %d partitions", len(stored), len(parts))
	}
	got := 0
	for _, sb := range stored {
		o := sb.Batch.Origin
		if sb.MsgID != stream.EventsMsgID(w.TenantId, s.id, 1, sb.Partition) || sb.Batch.BatchSeq != 1 || sb.Batch.Replayed {
			t.Fatalf("batch %s %+v", sb.MsgID, sb.Batch)
		}
		if o.TenantId != w.TenantId || o.ServerId != s.id || o.ServerName != "Hub-01" || o.NetworkId != "net" ||
			o.ConnectionId != w.ConnectionId || !o.ShadowMode || o.Platform != pb.Platform_PLATFORM_PAPER || o.InstanceId != s.instance {
			t.Fatalf("origin %+v", o)
		}
		if o.ClockOffsetUs < -5_000_000 || o.ClockOffsetUs > 5_000_000 {
			t.Fatalf("clock offset %d", o.ClockOffsetUs)
		}
		if r := int64(sb.Batch.GatewayRecvUnixUs); r < before.Add(-time.Second).UnixMicro() || r > time.Now().UnixMicro() {
			t.Fatalf("gateway recv time %d", r)
		}
		// Each partition keeps the batch order of its players' events.
		last := -1
		for _, ev := range sb.Batch.Events {
			n, _ := e.d.Bus.Partitioner.ForPlayer(ev.Player)
			idx := indexOf(events, ev)
			if n != sb.Partition || idx <= last {
				t.Fatalf("event %d in partition %d (want %d) after %d", idx, sb.Partition, n, last)
			}
			last = idx
			got++
		}
	}
	if got != len(events) {
		t.Fatalf("stored %d of %d events", got, len(events))
	}

	// Resending the same seq (same or another connection) stores nothing new.
	sendBatch(t, c, b)
	c.Close()
	c2, _ := e.connect(s)
	b.Replayed = true
	sendBatch(t, c2, b)
	if n := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); n != uint64(len(parts)) {
		t.Fatalf("resent batch duplicated in the stream: %d messages", n)
	}
	if counterValue(e.d.Metrics.Batches.WithLabelValues(metrics.BatchDuplicate)) != 2 {
		t.Fatal("duplicates not counted")
	}
	// An empty batch is acked without publishing.
	sendBatch(t, c2, testkit.Batch(2))
	if n := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); n != uint64(len(parts)) {
		t.Fatalf("empty batch published: %d", n)
	}
}

func indexOf(events []*pb.Event, ev *pb.Event) int {
	for i, x := range events {
		if proto.Equal(x, ev) {
			return i
		}
	}
	return -1
}

func TestProbeRoutingAndStallFanOut(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	c, _ := e.connect(s)
	p := testkit.NewPlayer("Alice")
	now := uint64(time.Now().UnixMicro())
	env := &pb.Event{TimeUs: now, Kind: &pb.Event_CheckStats{CheckStats: &pb.CheckStats{CheckId: "server.level"}}}
	events := []*pb.Event{testkit.Probe(0, now), testkit.Probe(1, now), testkit.Stall(now-500_000, now, 0.4), testkit.Probe(2, now),
		p.Probe(7), testkit.Probe(13, now), env, p.Join()}
	sendBatch(t, c, testkit.Batch(1, events...))
	stored := testkit.EventBatches(t, e.d.Bus)
	if len(stored) != 4 {
		t.Fatalf("a stall must reach every partition: %d stream batches", len(stored))
	}
	serverPart, _ := e.d.Bus.Partitioner.ForServer(s.id)
	playerPart, _ := e.d.Bus.Partitioner.ForPlayer(p.UUID)
	for _, sb := range stored {
		stall := -1
		for i, ev := range sb.Batch.Events {
			switch k := ev.Kind.(type) {
			case *pb.Event_Probe:
				if int(k.Probe.ProbeId%4) != sb.Partition {
					t.Fatalf("probe %d in partition %d", k.Probe.ProbeId, sb.Partition)
				}
				if k.Probe.ProbeId == 2 && i < stall {
					t.Fatal("order across the stall")
				}
			case *pb.Event_Stall:
				stall = i
			case *pb.Event_CheckStats:
				if sb.Partition != serverPart {
					t.Fatal("server-level event not in the server's partition")
				}
			case *pb.Event_Join:
				if sb.Partition != playerPart {
					t.Fatal("player event in the wrong partition")
				}
			}
		}
		if stall < 0 {
			t.Fatalf("partition %d has no stall", sb.Partition)
		}
	}
}

func TestReplayDetectionAndClockOffset(t *testing.T) {
	e := newEnv(t, fastOptions())
	s := newServer(keyPro)
	// The plugin clock runs 3 s behind the cloud.
	skew := 3 * time.Second
	c, _ := e.connect(s, func(h *pb.Hello) { h.ClientTimeUnixMs = uint64(time.Now().Add(-skew).UnixMilli()) })
	ev := testkit.Probe(1, 1)
	live := &pb.Batch{Seq: 1, SentUnixMs: uint64(time.Now().Add(-skew).UnixMilli()), Events: []*pb.Event{ev}}
	flagged := &pb.Batch{Seq: 2, SentUnixMs: uint64(time.Now().Add(-skew).UnixMilli()), Replayed: true, Events: []*pb.Event{ev}}
	old := &pb.Batch{Seq: 3, SentUnixMs: uint64(time.Now().Add(-skew - time.Minute).UnixMilli()), Events: []*pb.Event{ev}}
	for _, b := range []*pb.Batch{live, flagged, old} {
		sendBatch(t, c, b)
	}
	want := map[uint64]bool{1: false, 2: true, 3: true}
	for _, sb := range testkit.EventBatches(t, e.d.Bus) {
		if sb.Batch.Replayed != want[sb.Batch.BatchSeq] {
			t.Errorf("seq %d replayed=%v", sb.Batch.BatchSeq, sb.Batch.Replayed)
		}
		off := time.Duration(sb.Batch.Origin.ClockOffsetUs) * time.Microsecond
		if off < skew-time.Second || off > skew+time.Second {
			t.Errorf("seq %d clock offset %v, want about %v", sb.Batch.BatchSeq, off, skew)
		}
	}
}

// expectPoison sends msg and expects Error{CODE_TOO_LARGE, fatal=false} followed by BatchAck{seq}.
func expectPoison(t *testing.T, c *testkit.Client, msg []byte, seq uint64) {
	t.Helper()
	ctx := ctxT(t, 30*time.Second)
	if err := c.SendRaw(ctx, msg); err != nil {
		t.Fatal(err)
	}
	f, err := c.Read(ctx)
	if err != nil || f.GetError().GetCode() != pb.Error_CODE_TOO_LARGE || f.GetError().Fatal {
		t.Fatalf("want a non-fatal TOO_LARGE, got %v %v", f, err)
	}
	f, err = c.Read(ctx)
	if err != nil || f.GetBatchAck().GetSeq() != seq {
		t.Fatalf("want BatchAck %d, got %v %v", seq, f, err)
	}
}

func encodeBatch(t *testing.T, b *pb.Batch, codecByte byte) []byte {
	t.Helper()
	msg, err := codec.EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: b}}, codecByte)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestPoisonBatchesAreAckedAndDropped(t *testing.T) {
	e := newEnv(t, fastOptions())
	c, _ := e.connect(newServer(keyPro))
	p := testkit.NewPlayer("Alice")

	badPlayer := testkit.Batch(1, &pb.Event{Player: []byte{1, 2, 3}, TimeUs: 1, Kind: &pb.Event_Swing{Swing: &pb.Swing{}}})
	expectPoison(t, c, encodeBatch(t, badPlayer, codec.Raw), 1)

	many := testkit.Batch(2)
	for i := 0; i < codec.DefaultMaxBatchEvents+1; i++ {
		many.Events = append(many.Events, p.Swing())
	}
	expectPoison(t, c, encodeBatch(t, many, codec.Zstd), 2)
	many.Seq = 3
	expectPoison(t, c, encodeBatch(t, many, codec.Deflate), 3)

	history := p.Attack(p)
	for i := 0; i < codec.MaxTargetHistory; i++ {
		history.GetAttack().TargetHistory = append(history.GetAttack().TargetHistory, &pb.TimedPos{TimeUs: uint64(i)})
	}
	expectPoison(t, c, encodeBatch(t, testkit.Batch(4, history), codec.Raw), 4)

	huge := p.Flag("movement.speed", pb.Category_CATEGORY_MOVEMENT, 1)
	huge.GetFlag().Info = strings.Repeat("x", codec.MaxEventBytes)
	expectPoison(t, c, encodeBatch(t, testkit.Batch(5, huge), codec.Zstd), 5)

	if n := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); n != 0 {
		t.Fatalf("poison batches reached the stream: %d", n)
	}
	sendBatch(t, c, testkit.Batch(6, p.Join()))
	if counterValue(e.d.Metrics.Batches.WithLabelValues(metrics.BatchTooLarge)) != 4 ||
		counterValue(e.d.Metrics.Batches.WithLabelValues(metrics.BatchInvalid)) != 1 {
		t.Fatal("poison counters")
	}
}

// streamFrame writes ClientFrame{batch: Batch{seq, events: [Event{flag: LocalFlag{info: n x 'a'}}]}}
// into w without holding it in memory.
func streamFrame(t *testing.T, w interface{ Write([]byte) (int, error) }, seq uint64, n int) {
	t.Helper()
	flag := protowire.SizeTag(6) + protowire.SizeVarint(uint64(n)) + n
	event := protowire.SizeTag(20) + protowire.SizeVarint(uint64(flag)) + flag
	batch := protowire.SizeTag(1) + protowire.SizeVarint(seq) + protowire.SizeTag(4) + protowire.SizeVarint(uint64(event)) + event
	var h []byte
	h = protowire.AppendTag(h, 2, protowire.BytesType)
	h = protowire.AppendVarint(h, uint64(batch))
	h = protowire.AppendTag(h, 1, protowire.VarintType)
	h = protowire.AppendVarint(h, seq)
	h = protowire.AppendTag(h, 4, protowire.BytesType)
	h = protowire.AppendVarint(h, uint64(event))
	h = protowire.AppendTag(h, 20, protowire.BytesType)
	h = protowire.AppendVarint(h, uint64(flag))
	h = protowire.AppendTag(h, 6, protowire.BytesType)
	h = protowire.AppendVarint(h, uint64(n))
	if _, err := w.Write(h); err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for n > 0 {
		k := min(n, len(chunk))
		if _, err := w.Write(chunk[:k]); err != nil {
			t.Fatal(err)
		}
		n -= k
	}
}

func deflateBomb(t *testing.T, seq uint64, n int) []byte {
	var buf bytes.Buffer
	buf.WriteByte(codec.Deflate)
	fw, _ := flate.NewWriter(&buf, flate.BestSpeed)
	streamFrame(t, fw, seq, n)
	fw.Close()
	return buf.Bytes()
}

func zstdBomb(t *testing.T, seq uint64, n int, declare bool) []byte {
	var buf bytes.Buffer
	buf.WriteByte(codec.Zstd)
	zw, _ := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if declare {
		zw.ResetContentSize(&buf, int64(n+64))
	}
	streamFrame(t, zw, seq, n)
	zw.Close()
	return buf.Bytes()
}

// heapGrowth runs fn and returns how much the process allocated meanwhile.
func heapGrowth(fn func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestDecompressionBombsRejectedWithoutOOM(t *testing.T) {
	e := newEnv(t, fastOptions())
	const bomb = 96 << 20 // decompressed size of every bomb
	for _, tc := range []struct {
		name string
		msg  []byte
	}{
		{"deflate", deflateBomb(t, 7, bomb)},
		{"zstd declared size", zstdBomb(t, 8, bomb, true)},
		{"zstd without size", zstdBomb(t, 9, bomb, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.msg) > codec.MaxCompressed {
				t.Fatalf("bomb of %d bytes exceeds the read limit", len(tc.msg))
			}
			c, _ := e.connect(newServer(keyPro))
			ctx := ctxT(t, 30*time.Second)
			var f *pb.ServerFrame
			var err error
			var answer pb.Error_Code
			grew := heapGrowth(func() {
				if err = c.SendRaw(ctx, tc.msg); err == nil {
					f, err = c.Read(ctx)
				}
			})
			if grew > 48<<20 {
				t.Fatalf("decoding a %d MiB bomb allocated %d MiB", bomb>>20, grew>>20)
			}
			answer = f.GetError().GetCode()
			switch {
			case err != nil:
				t.Fatalf("read: %v", err)
			case f.GetError().GetCode() == pb.Error_CODE_TOO_LARGE && !f.GetError().Fatal:
				if f, err = c.Read(ctx); err != nil || f.GetBatchAck() == nil {
					t.Fatalf("poison bomb must be acked: %v %v", f, err)
				}
			case f.GetError().GetCode() == pb.Error_CODE_BAD_REQUEST && f.GetError().Fatal:
			default:
				t.Fatalf("unexpected answer %v", f)
			}
			t.Logf("%s: %d compressed bytes, answer %v, allocated %d KiB", tc.name, len(tc.msg), answer, grew>>10)
		})
	}
	if n := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); n != 0 {
		t.Fatalf("bombs reached the stream: %d", n)
	}
	c, _ := e.connect(newServer(keyPro))
	sendBatch(t, c, testkit.Batch(1, testkit.Probe(1, 1)))
}

func TestProtobufAmplificationRejected(t *testing.T) {
	e := newEnv(t, fastOptions())
	c, _ := e.connect(newServer(keyPro))
	// 450 000 empty events in under 1 MiB: unmarshalled they would cost tens of MiB.
	const events = 450_000
	var batch []byte
	batch = protowire.AppendTag(batch, 1, protowire.VarintType)
	batch = protowire.AppendVarint(batch, 11)
	for i := 0; i < events; i++ {
		batch = protowire.AppendTag(batch, 4, protowire.BytesType)
		batch = protowire.AppendVarint(batch, 0)
	}
	msg := []byte{codec.Raw}
	msg = protowire.AppendTag(msg, 2, protowire.BytesType)
	msg = protowire.AppendBytes(msg, batch)
	grew := heapGrowth(func() { expectPoison(t, c, msg, 11) })
	if grew > 24<<20 {
		t.Fatalf("amplified: allocated %d MiB", grew>>20)
	}

	// Repeated batch fields are merged by protobuf: the pre-scan counts them together.
	one := encodeBatch(t, testkit.Batch(12, testkit.Probe(1, 1), testkit.Probe(2, 2), testkit.Probe(3, 3)), codec.Raw)[1:]
	merged := []byte{codec.Raw}
	for i := 0; i < 2000; i++ {
		merged = append(merged, one...)
	}
	expectPoison(t, c, merged, 12)
	if n := testkit.StreamMessages(t, e.d.Bus, stream.StreamEvents); n != 0 {
		t.Fatalf("amplified batches reached the stream: %d", n)
	}
}

func TestPeekBatchSeq(t *testing.T) {
	b := &pb.Batch{Seq: 123456789, SentUnixMs: 1, Replayed: true, Events: []*pb.Event{testkit.Probe(1, 1)}}
	for _, c := range []byte{codec.Raw, codec.Zstd, codec.Deflate} {
		msg, err := codec.EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: b}}, c)
		if err != nil {
			t.Fatal(err)
		}
		if seq, ok := peekBatchSeq(msg); !ok || seq != b.Seq {
			t.Fatalf("codec %d: %d %v", c, seq, ok)
		}
	}
	// Fields in another order (sent_unix_ms first) are still found.
	var raw []byte
	raw = protowire.AppendTag(raw, 2, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 99)
	raw = protowire.AppendTag(raw, 1, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 5)
	frame := protowire.AppendBytes(protowire.AppendTag([]byte{codec.Raw}, 2, protowire.BytesType), raw)
	if seq, ok := peekBatchSeq(frame); !ok || seq != 5 {
		t.Fatal("reordered fields")
	}
	for _, bad := range [][]byte{nil, {0}, {codec.Raw, 0x0a, 0x00}, {0x09, 1, 2}, {codec.Zstd, 1, 2, 3}, {codec.Deflate, 0xff},
		protowire.AppendBytes(protowire.AppendTag([]byte{codec.Raw}, 2, protowire.BytesType), protowire.AppendTag(nil, 4, protowire.BytesType))} {
		if _, ok := peekBatchSeq(bad); ok {
			t.Errorf("found a seq in %x", bad)
		}
	}
	ping, _ := codec.EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Ping{Ping: &pb.Ping{Nonce: 1}}}, codec.Raw)
	if _, ok := peekBatchSeq(ping); ok {
		t.Error("a ping has no batch seq")
	}
}
