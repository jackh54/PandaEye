package codec

import (
	"context"
	"testing"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func FuzzDecode(f *testing.F) {
	for _, c := range []byte{Raw, Zstd, Deflate} {
		msg, _ := EncodeClientFrame(sampleBatch(3), c)
		f.Add(msg)
	}
	hello, _ := EncodeClientFrame(&pb.ClientFrame{Kind: &pb.ClientFrame_Hello{Hello: &pb.Hello{ServerId: "s", Capabilities: []string{"probe"}}}}, Raw)
	f.Add(hello)
	f.Add(append([]byte{Zstd}, zstdHeader(500<<20, false)...))
	f.Add(append([]byte{Raw}, emptyEvents(10)...))
	f.Add([]byte{Deflate, 0x01, 0x00, 0x00, 0xff, 0xff})

	d, err := NewDecoder(Limits{MaxBatchEvents: 64})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(d.Close)
	f.Fuzz(func(t *testing.T, msg []byte) {
		fr, err := d.Decode(context.Background(), msg)
		if err != nil {
			return
		}
		if b := fr.GetBatch(); b != nil && len(b.Events) > 64 {
			t.Fatalf("decoded %d events past the limit", len(b.Events))
		}
		for _, e := range fr.GetBatch().GetEvents() {
			if len(e.Player) != 0 && len(e.Player) != 16 {
				t.Fatal("invalid player length passed validation")
			}
			if fl := e.GetFlag(); fl != nil && len(fl.Info) > MaxInfoBytes {
				t.Fatal("info not truncated")
			}
		}
		_, _ = d.DecodeHello(msg)
	})
}
