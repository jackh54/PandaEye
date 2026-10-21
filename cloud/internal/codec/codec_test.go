package codec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func newDecoder(t testing.TB, lim Limits) *Decoder {
	t.Helper()
	d, err := NewDecoder(lim)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

var player = bytes.Repeat([]byte{7}, 16)

func sampleBatch(n int) *pb.ClientFrame {
	b := &pb.Batch{Seq: 9, SentUnixMs: 1700000000000}
	for i := 0; i < n; i++ {
		b.Events = append(b.Events, &pb.Event{
			Player: player, TimeUs: uint64(i), Tick: uint32(i),
			Kind: &pb.Event_Move{Move: &pb.MoveSample{X: float64(i), Y: 64, Z: 1, HasPos: true, OnGround: true}},
		})
	}
	return &pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: b}}
}

// allocPerOp returns the average number of bytes allocated by fn.
func allocPerOp(n int, fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		fn()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

func TestRoundTripAllCodecs(t *testing.T) {
	d := newDecoder(t, Limits{})
	in := sampleBatch(500)
	for _, c := range []byte{Raw, Zstd, Deflate} {
		msg, err := EncodeClientFrame(in, c)
		if err != nil {
			t.Fatal(err)
		}
		if msg[0] != c {
			t.Fatalf("codec byte %#x", msg[0])
		}
		out, err := d.Decode(context.Background(), msg)
		if err != nil {
			t.Fatalf("codec %#x: %v", c, err)
		}
		if !proto.Equal(in, out) {
			t.Fatalf("codec %#x: round trip mismatch", c)
		}
	}
}

func TestServerFrameRaw(t *testing.T) {
	d := newDecoder(t, Limits{})
	f := &pb.ServerFrame{Kind: &pb.ServerFrame_BatchAck{BatchAck: &pb.BatchAck{Seq: 42}}}
	msg, err := EncodeServerFrame(f)
	if err != nil || msg[0] != Raw {
		t.Fatalf("encode: %v %v", msg, err)
	}
	got, err := d.DecodeServerFrame(msg)
	if err != nil || got.GetBatchAck().GetSeq() != 42 {
		t.Fatalf("decode: %v %v", got, err)
	}
}

func TestHello(t *testing.T) {
	d := newDecoder(t, Limits{})
	hello := &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: &pb.Hello{ProtocolVersion: 1, ServerId: "x", ServerName: strings.Repeat("n", 300)}}}
	raw, _ := EncodeClientFrame(hello, Raw)
	h, err := d.DecodeHello(raw)
	if err != nil || h.ServerId != "x" || len(h.ServerName) != MaxStringBytes {
		t.Fatalf("hello: %v %v", h, err)
	}
	if _, err := d.Decode(context.Background(), raw); err != nil {
		t.Fatalf("hello via Decode: %v", err)
	}
	z, _ := EncodeClientFrame(hello, Zstd)
	if _, err := d.DecodeHello(z); !errors.Is(err, ErrMalformed) {
		t.Fatalf("compressed hello: %v", err)
	}
	if _, err := d.Decode(context.Background(), z); !errors.Is(err, ErrMalformed) {
		t.Fatalf("compressed hello via Decode: %v", err)
	}
	big := &pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: &pb.Hello{ProtocolVersion: 1}}}
	bigRaw, _ := proto.Marshal(big)
	bigRaw = protowire.AppendTag(bigRaw, 99, protowire.BytesType)
	bigRaw = protowire.AppendBytes(bigRaw, make([]byte, MaxHello))
	if _, err := d.DecodeHello(append([]byte{Raw}, bigRaw...)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized hello: %v", err)
	}
	batch, _ := EncodeClientFrame(sampleBatch(1), Raw)
	if _, err := d.DecodeHello(batch); !errors.Is(err, ErrMalformed) {
		t.Fatalf("batch as first frame: %v", err)
	}
	caps := &pb.Hello{}
	for i := 0; i <= MaxCapabilities; i++ {
		caps.Capabilities = append(caps.Capabilities, "c")
	}
	capsRaw, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: caps}}, Raw)
	if _, err := d.DecodeHello(capsRaw); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too many capabilities: %v", err)
	}
}

func TestBasicRejections(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	for name, c := range map[string]struct {
		msg  []byte
		want error
	}{
		"empty":             {nil, ErrMalformed},
		"unknown codec":     {[]byte{0x03, 1, 2}, ErrUnknownCodec},
		"empty frame":       {[]byte{Raw}, ErrMalformed},
		"garbage":           {[]byte{Raw, 0xff, 0xff, 0xff}, ErrMalformed},
		"too large":         {make([]byte, MaxCompressed+2), ErrTooLarge},
		"bad zstd magic":    {append([]byte{Zstd}, make([]byte, 20)...), ErrMalformed},
		"truncated deflate": {[]byte{Deflate, 0x05}, ErrMalformed},
	} {
		if _, err := d.Decode(ctx, c.msg); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
}

// zstdHeader builds a single-segment frame header declaring size with a 4-byte content size field.
func zstdHeader(size uint32, checksum bool) []byte {
	fhd := byte(0x80 | 0x20) // FCS 4 bytes, single segment
	if checksum {
		fhd |= 0x04
	}
	b := binary.LittleEndian.AppendUint32(nil, zstdMagic)
	b = append(b, fhd)
	return binary.LittleEndian.AppendUint32(b, size)
}

func rleBlock(size int, last bool) []byte {
	h := uint32(size)<<3 | 1<<1
	if last {
		h |= 1
	}
	return []byte{byte(h), byte(h >> 8), byte(h >> 16), 0x00}
}

func TestZstdDecompressionBomb(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	// 17 bytes: header (9) + one RLE block (4) + checksum (4), declaring 500 MiB.
	bomb := append(zstdHeader(500<<20, true), rleBlock(128<<10, true)...)
	bomb = append(bomb, 0, 0, 0, 0)
	if len(bomb) != 17 {
		t.Fatalf("bomb is %d bytes", len(bomb))
	}
	msg := append([]byte{Zstd}, bomb...)
	if _, err := d.Decode(ctx, msg); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bomb: %v", err)
	}
	if a := allocPerOp(200, func() { _, _ = d.Decode(ctx, msg) }); a > 4<<10 {
		t.Fatalf("rejecting the bomb allocated %d bytes per call", a)
	}

	// A frame that lies about its size: declares 100 bytes, RLE expands to 128 KiB.
	liar := append([]byte{Zstd}, zstdHeader(100, false)...)
	liar = append(liar, rleBlock(128<<10, true)...)
	if _, err := d.Decode(ctx, liar); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("lying frame: %v", err)
	}
	if a := allocPerOp(50, func() { _, _ = d.Decode(ctx, liar) }); a > 64<<10 {
		t.Fatalf("lying frame allocated %d bytes per call", a)
	}

	// Many RLE blocks declaring a total far beyond the content size.
	many := append([]byte{Zstd}, zstdHeader(8<<20, false)...)
	for i := 0; i < 1000; i++ {
		many = append(many, rleBlock(128<<10, i == 999)...)
	}
	if _, err := d.Decode(ctx, many); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("RLE amplification: %v", err)
	}
}

func TestZstdFrameRules(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	raw, _ := proto.Marshal(sampleBatch(10))
	frame := CompressZstd(raw)
	if _, err := checkZstdFrame(frame, MaxDecompressed); err != nil {
		t.Fatalf("own frame rejected: %v", err)
	}
	cases := map[string][]byte{
		"two frames":      append(append([]byte{}, frame...), frame...),
		"trailing bytes":  append(append([]byte{}, frame...), 0),
		"truncated":       frame[:len(frame)-1],
		"skippable frame": append(binary.LittleEndian.AppendUint32(nil, 0x184D2A50), 0, 0, 0, 0),
	}
	noSize := binary.LittleEndian.AppendUint32(nil, zstdMagic)
	noSize = append(noSize, 0x00, 0x58) // no FCS, window descriptor
	cases["no content size"] = append(noSize, rleBlock(10, true)...)
	dict := binary.LittleEndian.AppendUint32(nil, zstdMagic)
	cases["dictionary"] = append(dict, 0x21, 1, 10, 0, 0, 0)
	for name, b := range cases {
		if _, err := d.Decode(ctx, append([]byte{Zstd}, b...)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	bigWindow := binary.LittleEndian.AppendUint32(nil, zstdMagic)
	bigWindow = append(bigWindow, 0x80, 0xF8, 10, 0, 0, 0) // window 2^41
	if _, err := d.Decode(ctx, append([]byte{Zstd}, bigWindow...)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge window: %v", err)
	}
}

func TestDeflateBomb(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	zeros := make([]byte, 64<<20)
	comp, err := compressDeflate(zeros)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp) > MaxCompressed {
		t.Fatalf("bomb does not fit the compressed limit: %d", len(comp))
	}
	msg := append([]byte{Deflate}, comp...)
	if _, err := d.Decode(ctx, msg); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("deflate bomb: %v", err)
	}
	if a := allocPerOp(3, func() { _, _ = d.Decode(ctx, msg) }); a > 2*MaxDecompressed+(2<<20) {
		t.Fatalf("deflate bomb allocated %d bytes per call", a)
	}
	ok, _ := EncodeClientFrame(sampleBatch(3), Deflate)
	if _, err := d.Decode(ctx, append(ok, 0x00)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("trailing data after deflate: %v", err)
	}
}

// emptyEvents returns a ClientFrame whose batch holds n empty events (2 bytes each).
func emptyEvents(n int) []byte {
	var batch []byte
	for i := 0; i < n; i++ {
		batch = protowire.AppendTag(batch, fBatchEvents, protowire.BytesType)
		batch = protowire.AppendBytes(batch, nil)
	}
	f := protowire.AppendTag(nil, fClientFrameBatch, protowire.BytesType)
	return protowire.AppendBytes(f, batch)
}

func TestProtobufAmplification(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	// 1M empty events = 2 MiB on the wire, ~200 MiB of Go objects if unmarshalled.
	msg := append([]byte{Zstd}, CompressZstd(emptyEvents(1_000_000))...)
	if _, err := d.Decode(ctx, msg); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("event flood: %v", err)
	}
	if a := allocPerOp(3, func() { _, _ = d.Decode(ctx, msg) }); a > 4<<20 {
		t.Fatalf("event flood allocated %d bytes per call", a)
	}

	// Two batch fields are merged by protobuf: their events add up.
	half := emptyEvents(3000)
	merged := append(append([]byte{Raw}, half...), half...)
	if _, err := d.Decode(ctx, merged); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("merged batches: %v", err)
	}
	if f, err := d.Decode(ctx, append([]byte{Raw}, emptyEvents(DefaultMaxBatchEvents)...)); err != nil || len(f.GetBatch().Events) != DefaultMaxBatchEvents {
		t.Fatalf("batch of exactly max_batch_events: %v", err)
	}

	// Nested amplification: one attack with 10 000 empty target_history entries.
	var attack []byte
	for i := 0; i < 10_000; i++ {
		attack = protowire.AppendTag(attack, fAttackTargetHistory, protowire.BytesType)
		attack = protowire.AppendBytes(attack, nil)
	}
	ev := protowire.AppendTag(nil, fEventAttack, protowire.BytesType)
	ev = protowire.AppendBytes(ev, attack)
	batch := protowire.AppendTag(nil, fBatchEvents, protowire.BytesType)
	batch = protowire.AppendBytes(batch, ev)
	frame := protowire.AppendTag(nil, fClientFrameBatch, protowire.BytesType)
	frame = protowire.AppendBytes(frame, batch)
	if _, err := d.Decode(ctx, append([]byte{Raw}, frame...)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("target_history flood: %v", err)
	}

	// One event larger than 64 KiB.
	bigEv := protowire.AppendTag(nil, 99, protowire.BytesType)
	bigEv = protowire.AppendBytes(bigEv, make([]byte, MaxEventBytes))
	batch = protowire.AppendTag(nil, fBatchEvents, protowire.BytesType)
	batch = protowire.AppendBytes(batch, bigEv)
	frame = protowire.AppendTag(nil, fClientFrameBatch, protowire.BytesType)
	frame = protowire.AppendBytes(frame, batch)
	if _, err := d.Decode(ctx, append([]byte{Zstd}, CompressZstd(frame)...)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized event: %v", err)
	}
}

func TestMaxBatchEventsLimit(t *testing.T) {
	d := newDecoder(t, Limits{MaxBatchEvents: 10})
	ok, _ := EncodeClientFrame(sampleBatch(10), Raw)
	if _, err := d.Decode(context.Background(), ok); err != nil {
		t.Fatalf("10 events: %v", err)
	}
	over, _ := EncodeClientFrame(sampleBatch(11), Raw)
	if _, err := d.Decode(context.Background(), over); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("11 events: %v", err)
	}
}

func TestFieldLimitsAndSanitising(t *testing.T) {
	d := newDecoder(t, Limits{})
	ctx := context.Background()
	flag := &pb.LocalFlag{CheckId: "movement.speed", Info: strings.Repeat("é", 400), Data: map[string]float64{strings.Repeat("k", 300): 1}}
	f := &pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: &pb.Batch{Events: []*pb.Event{
		{Player: player, Kind: &pb.Event_Flag{Flag: flag}},
		{Player: player, Kind: &pb.Event_Join{Join: &pb.PlayerJoin{Name: strings.Repeat("a", 1000)}}},
	}}}}
	msg, _ := EncodeClientFrame(f, Raw)
	out, err := d.Decode(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	got := out.GetBatch().Events[0].GetFlag()
	if len(got.Info) > MaxInfoBytes || len(got.Info) < MaxInfoBytes-1 || !utf8Valid(got.Info) {
		t.Fatalf("info truncated to %d bytes", len(got.Info))
	}
	for k := range got.Data {
		if len(k) != MaxStringBytes {
			t.Fatalf("data key %d bytes", len(k))
		}
	}
	join := out.GetBatch().Events[1].GetJoin()
	if len(join.Name) != MaxStringBytes {
		t.Fatalf("join name truncated to %d", len(join.Name))
	}

	for name, ev := range map[string]*pb.Event{
		"short player": {Player: []byte{1, 2, 3}, Kind: &pb.Event_Swing{Swing: &pb.Swing{}}},
		"short target": {Player: player, Kind: &pb.Event_Attack{Attack: &pb.Attack{TargetPlayer: []byte{1}}}},
	} {
		m, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: &pb.Batch{Events: []*pb.Event{ev}}}}, Raw)
		if _, err := d.Decode(ctx, m); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}

	data := map[string]float64{}
	for i := 0; i <= MaxDataEntries; i++ {
		data[string(rune('a'+i%26))+strings.Repeat("x", i)] = 1
	}
	m, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Batch{Batch: &pb.Batch{Events: []*pb.Event{
		{Player: player, Kind: &pb.Event_Flag{Flag: &pb.LocalFlag{Data: data}}}}}}}, Raw)
	if _, err := d.Decode(ctx, m); !errors.Is(err, ErrTooLarge) {
		t.Errorf("33 data entries: %v", err)
	}
}

func TestInvalidUTF8IsReplaced(t *testing.T) {
	d := newDecoder(t, Limits{})
	join := protowire.AppendTag(nil, 1, protowire.BytesType) // PlayerJoin.name
	join = protowire.AppendBytes(join, []byte("bad\xff\xfename"))
	ev := protowire.AppendTag(nil, 1, protowire.BytesType)
	ev = protowire.AppendBytes(ev, player)
	ev = protowire.AppendTag(ev, 10, protowire.BytesType) // Event.join
	ev = protowire.AppendBytes(ev, join)
	batch := protowire.AppendTag(nil, fBatchEvents, protowire.BytesType)
	batch = protowire.AppendBytes(batch, ev)
	frame := protowire.AppendTag(nil, fClientFrameBatch, protowire.BytesType)
	frame = protowire.AppendBytes(frame, batch)
	for _, c := range []byte{Raw, Zstd} {
		msg, _ := Encode(frame, c)
		out, err := d.Decode(context.Background(), msg)
		if err != nil {
			t.Fatalf("codec %#x: %v", c, err)
		}
		if got := out.GetBatch().Events[0].GetJoin().Name; got != "bad�name" {
			t.Fatalf("name %q", got)
		}
	}
	hello := protowire.AppendTag(nil, 4, protowire.BytesType)
	hello = protowire.AppendBytes(hello, []byte{0xc3})
	hf := protowire.AppendTag(nil, 1, protowire.BytesType)
	hf = protowire.AppendBytes(hf, hello)
	h, err := d.DecodeHello(append([]byte{Raw}, hf...))
	if err != nil || h.ServerName != "�" {
		t.Fatalf("hello sanitising: %q %v", h.GetServerName(), err)
	}
}

func TestInFlightSemaphoreBlocks(t *testing.T) {
	d := newDecoder(t, Limits{MaxInFlight: MaxDecompressed + 1})
	if err := d.sem.Acquire(context.Background(), MaxDecompressed+1); err != nil {
		t.Fatal(err)
	}
	msg, _ := EncodeClientFrame(sampleBatch(5), Zstd)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := d.Decode(ctx, msg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected to block on the semaphore: %v", err)
	}
	d.sem.Release(MaxDecompressed + 1)
	if _, err := d.Decode(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if !d.sem.TryAcquire(MaxDecompressed + 1) {
		t.Fatal("decode leaked semaphore weight")
	}
}

func TestDecodeZstdClip(t *testing.T) {
	d := newDecoder(t, Limits{})
	data := bytes.Repeat([]byte("clip"), 1000)
	out, err := d.DecodeZstd(context.Background(), CompressZstd(data), 1<<20)
	if err != nil || !bytes.Equal(out, data) {
		t.Fatalf("clip: %v", err)
	}
	if _, err := d.DecodeZstd(context.Background(), CompressZstd(data), 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("clip over max: %v", err)
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }
