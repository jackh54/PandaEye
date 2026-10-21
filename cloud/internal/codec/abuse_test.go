package codec

import (
	"context"
	"errors"
	"runtime"
	"testing"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestNULStrippedFromStrings(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	msg, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: &pb.Hello{ServerName: "a\x00b"}}}, Raw)
	h, err := d.DecodeHello(msg)
	if err != nil || h.ServerName != "ab" {
		t.Fatalf("server_name %q err %v", h.GetServerName(), err)
	}
}

// A tiny zstd frame that expands to millions of empty hello capabilities is rejected before
// protobuf allocates them.
func TestCompressedHelloRejectedBeforeUnmarshal(t *testing.T) {
	d, _ := NewDecoder(Limits{})
	defer d.Close()
	var hello []byte
	for len(hello) < 8<<20-16 {
		hello = protowire.AppendTag(hello, 12, protowire.BytesType)
		hello = protowire.AppendBytes(hello, nil)
	}
	raw := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), hello)
	msg, err := Encode(raw, Zstd)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = d.Decode(context.Background(), msg)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("decode allocated %d MiB", alloc>>20)
	}
}
