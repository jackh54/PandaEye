package codec

import (
	"context"
	"runtime"
	"testing"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestReviewNULPassesThrough(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	msg, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: &pb.Hello{ServerName: "a\x00b"}}}, Raw)
	h, err := d.DecodeHello(msg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("server_name after decode: %q", h.ServerName)
}

func TestReviewHelloCapabilitiesAmplification(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	// ClientFrame{hello{capabilities: "" x N}} serialised by hand.
	var hello []byte
	for len(hello) < 8<<20-16 {
		hello = protowire.AppendTag(hello, 12, protowire.BytesType)
		hello = protowire.AppendBytes(hello, nil)
	}
	raw := protowire.AppendTag(nil, 1, protowire.BytesType)
	raw = protowire.AppendBytes(raw, hello)
	msg, err := Encode(raw, Zstd)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("compressed %d bytes, raw %d bytes", len(msg), len(raw))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = d.Decode(context.Background(), msg)
	runtime.ReadMemStats(&after)
	t.Logf("decode err=%v total alloc during decode: %d MiB", err, (after.TotalAlloc-before.TotalAlloc)>>20)
}

func TestReviewSmallZstd(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	f := &pb.ClientFrame{Kind: &pb.ClientFrame_Goodbye{Goodbye: &pb.Goodbye{Reason: "x"}}}
	msg, _ := EncodeClientFrame(f, Zstd)
	if _, err := d.Decode(context.Background(), msg); err != nil {
		t.Fatalf("small zstd: %v", err)
	}
}

func TestReviewDecodeZstdAboveInFlight(t *testing.T) {
	d, _ := NewDecoder(Limits{MaxInFlight: 1 << 20})
	defer d.Close()
	frame := CompressZstd(make([]byte, 2<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.DecodeZstd(ctx, frame, 4<<20)
	t.Logf("DecodeZstd(2 MiB, max 4 MiB, in-flight cap %d) err=%v after %v", d.Limits().MaxInFlight, err, time.Since(start).Round(time.Millisecond))
}

func TestReviewDecodeZstdHugeMax(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	frame := append(zstdHeader(300<<20, false), rleBlock(128<<10, true)...)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	start := time.Now()
	_, err := d.DecodeZstd(ctx, frame, 512<<20)
	t.Logf("err=%v after %v", err, time.Since(start).Round(time.Millisecond))
}
