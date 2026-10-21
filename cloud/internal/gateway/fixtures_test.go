package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/testkit"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"
)

// fixtureDir holds the codec fixtures written by the Java plugin (test/fixtures/README.md).
var fixtureDir = filepath.Join("..", "..", "..", "test", "fixtures")

type fixture struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Seq    uint64 `json:"seq"`
	Events int    `json:"events"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("Java codec fixtures not found in %s (generate them with `gradle -p plugin :common:writeFixtures`)", fixtureDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	var fx []fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx) == 0 {
		t.Skip("the fixture manifest is empty")
	}
	return fx
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestJavaFixturesDecode checks that every Java-produced frame decodes, in every codec, to exactly
// the ClientFrame of its .pb file.
func TestJavaFixturesDecode(t *testing.T) {
	dec, err := codec.NewDecoder(codec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	for _, f := range loadFixtures(t) {
		pbBytes := readFixture(t, f.Name+".pb")
		sum := sha256.Sum256(pbBytes)
		if len(pbBytes) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("%s.pb does not match the manifest", f.Name)
		}
		var want pb.ClientFrame
		if err := proto.Unmarshal(pbBytes, &want); err != nil {
			t.Fatalf("%s.pb: %v", f.Name, err)
		}
		for _, ext := range []string{"raw", "zstd", "deflate"} {
			msg := readFixture(t, f.Name+"."+ext+".bin")
			if raw := decompress(t, msg); !bytes.Equal(raw, pbBytes) {
				t.Fatalf("%s.%s does not decompress to the .pb bytes", f.Name, ext)
			}
			if f.Kind == "hello" && ext != "raw" {
				// Hellos are only ever sent as codec 0x00; the gateway refuses compressed ones.
				if _, err := dec.Decode(context.Background(), msg); err == nil {
					t.Fatalf("%s.%s: a compressed hello must be refused", f.Name, ext)
				}
				continue
			}
			got, err := dec.Decode(context.Background(), msg)
			if err != nil {
				t.Fatalf("%s.%s: %v", f.Name, ext, err)
			}
			if !proto.Equal(got, &want) {
				t.Fatalf("%s.%s decodes to a different frame", f.Name, ext)
			}
		}
		switch f.Kind {
		case "hello":
			if want.GetHello() == nil {
				t.Fatalf("%s is not a hello", f.Name)
			}
		case "batch":
			if b := want.GetBatch(); b == nil || b.Seq != f.Seq || len(b.Events) != f.Events {
				t.Fatalf("%s: batch does not match the manifest", f.Name)
			}
		}
	}
}

// TestJavaFixturesThroughTheGateway replays the Java frames on a real connection: the Hello is
// admitted and every batch (raw, zstd and DEFLATE) is stored and acknowledged.
func TestJavaFixturesThroughTheGateway(t *testing.T) {
	fx := loadFixtures(t)
	var hello pb.ClientFrame
	if err := proto.Unmarshal(readFixture(t, "hello.pb"), &hello); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, fastOptions(), "PE_STATIC_LICENSES="+hello.GetHello().LicenseKey+"::pro")
	c := e.dial(nil)
	ctx := ctxT(t, 20*time.Second)
	if err := c.SendRaw(ctx, readFixture(t, "hello.raw.bin")); err != nil {
		t.Fatal(err)
	}
	f, err := c.Read(ctx)
	if err != nil || f.GetWelcome() == nil || f.GetWelcome().ServerSecret == "" {
		t.Fatalf("welcome: %v %v", f, err)
	}
	codecs := []string{"raw", "zstd", "deflate"}
	events := 0
	n := 0
	for _, x := range fx {
		if x.Kind != "batch" {
			continue
		}
		if err := c.SendRaw(ctx, readFixture(t, x.Name+"."+codecs[n%len(codecs)]+".bin")); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitBatchAck(ctx, x.Seq); err != nil {
			t.Fatalf("%s: %v", x.Name, err)
		}
		events += x.Events
		n++
	}
	stored := 0
	for _, sb := range testkit.EventBatches(t, e.d.Bus) {
		stored += len(sb.Batch.Events)
		if sb.Batch.Origin.ServerId != hello.GetHello().ServerId || sb.Batch.Origin.Platform != hello.GetHello().Platform {
			t.Fatalf("origin %+v", sb.Batch.Origin)
		}
	}
	if stored != events {
		t.Fatalf("stored %d of %d fixture events", stored, events)
	}
}

// decompress strips the codec byte and decompresses like a plugin receiving the frame.
func decompress(t *testing.T, msg []byte) []byte {
	t.Helper()
	switch msg[0] {
	case codec.Raw:
		return msg[1:]
	case codec.Zstd:
		d, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(codec.MaxDecompressed))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		out, err := d.DecodeAll(msg[1:], nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	case codec.Deflate:
		out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(msg[1:])), codec.MaxDecompressed))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Fatalf("unknown codec %#x", msg[0])
	return nil
}
