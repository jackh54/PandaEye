package codec

import (
	"context"
	"errors"
	"runtime"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
)

// Limits configures a Decoder. Zero fields take the protocol defaults.
type Limits struct {
	MaxCompressed   int   // payload bytes after the codec byte
	MaxDecompressed int   // decoded message bytes
	MaxHello        int   // Hello payload bytes
	MaxBatchEvents  int   // events per Batch (Welcome.max_batch_events)
	MaxEventBytes   int   // bytes per serialised Event
	MaxInFlight     int64 // decompressed bytes being decoded at once, process-wide
}

func (l Limits) withDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&l.MaxCompressed, MaxCompressed)
	def(&l.MaxDecompressed, MaxDecompressed)
	def(&l.MaxHello, MaxHello)
	def(&l.MaxBatchEvents, DefaultMaxBatchEvents)
	def(&l.MaxEventBytes, MaxEventBytes)
	if l.MaxInFlight <= 0 {
		l.MaxInFlight = DefaultMaxInFlightBytes
	}
	if l.MaxInFlight < int64(l.MaxDecompressed)+1 {
		l.MaxInFlight = int64(l.MaxDecompressed) + 1
	}
	return l
}

// Decoder decodes client frames. It is safe for concurrent use; one Decoder per process shares the
// in-flight semaphore and a pool of zstd decoders (each with concurrency 1).
type Decoder struct {
	lim  Limits
	sem  *semaphore.Weighted
	zstd chan *zstd.Decoder
}

// NewDecoder returns a Decoder with the given limits.
func NewDecoder(lim Limits) (*Decoder, error) {
	lim = lim.withDefaults()
	n := runtime.GOMAXPROCS(0)
	d := &Decoder{lim: lim, sem: semaphore.NewWeighted(lim.MaxInFlight), zstd: make(chan *zstd.Decoder, n)}
	for i := 0; i < n; i++ {
		zd, err := zstd.NewReader(nil,
			zstd.WithDecoderMaxMemory(uint64(lim.MaxDecompressed)),
			zstd.WithDecoderMaxWindow(uint64(lim.MaxDecompressed)),
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecodeAllCapLimit(true),
		)
		if err != nil {
			d.Close()
			return nil, err
		}
		d.zstd <- zd
	}
	return d, nil
}

// Limits returns the effective limits.
func (d *Decoder) Limits() Limits { return d.lim }

// Close releases the zstd decoders. The Decoder must not be used afterwards.
func (d *Decoder) Close() {
	for {
		select {
		case zd := <-d.zstd:
			zd.Close()
		default:
			return
		}
	}
}

// DecodeHello decodes the first frame of a connection: codec Raw, at most MaxHello bytes, a
// ClientFrame holding a Hello. String fields are truncated and the capability list is bounded.
func (d *Decoder) DecodeHello(msg []byte) (*pb.Hello, error) {
	if len(msg) == 0 {
		return nil, malformed("empty message")
	}
	if msg[0] != Raw {
		return nil, malformed("hello must use codec 0x00, got %#x", msg[0])
	}
	if len(msg)-1 > d.lim.MaxHello {
		return nil, tooLarge("hello of %d bytes exceeds %d", len(msg)-1, d.lim.MaxHello)
	}
	f, err := d.unmarshal(msg[1:])
	if err != nil {
		return nil, err
	}
	h := f.GetHello()
	if h == nil {
		return nil, malformed("first frame is not a hello")
	}
	return h, nil
}

// Decode decodes a ClientFrame in any codec under the decoder's limits. It blocks while the
// process-wide in-flight budget is exhausted (backpressure) until ctx is done.
func (d *Decoder) Decode(ctx context.Context, msg []byte) (*pb.ClientFrame, error) {
	raw, release, err := d.payload(ctx, msg)
	if err != nil {
		return nil, err
	}
	defer release()
	f, err := d.unmarshal(raw)
	if err != nil {
		return nil, err
	}
	if f.GetHello() != nil && (msg[0] != Raw || len(msg)-1 > d.lim.MaxHello) {
		return nil, malformed("hello must be codec 0x00 and <= %d bytes", d.lim.MaxHello)
	}
	return f, nil
}

// payload strips the codec byte and decompresses within limits. release returns the reserved
// in-flight bytes and must be called once the raw bytes are no longer used.
func (d *Decoder) payload(ctx context.Context, msg []byte) (raw []byte, release func(), err error) {
	if len(msg) == 0 {
		return nil, nil, malformed("empty message")
	}
	body := msg[1:]
	if len(body) > d.lim.MaxCompressed {
		return nil, nil, tooLarge("message of %d bytes exceeds %d", len(body), d.lim.MaxCompressed)
	}
	switch msg[0] {
	case Raw:
		return body, func() {}, nil
	case Zstd:
		size, err := checkZstdFrame(body, d.lim.MaxDecompressed)
		if err != nil {
			return nil, nil, err
		}
		release, err := d.reserve(ctx, int64(size))
		if err != nil {
			return nil, nil, err
		}
		raw, err := d.decodeZstd(body, size)
		if err != nil {
			release()
			return nil, nil, err
		}
		return raw, release, nil
	case Deflate:
		release, err := d.reserve(ctx, int64(d.lim.MaxDecompressed)+1)
		if err != nil {
			return nil, nil, err
		}
		raw, err := inflate(body, d.lim.MaxDecompressed)
		if err != nil {
			release()
			return nil, nil, err
		}
		return raw, release, nil
	default:
		return nil, nil, ErrUnknownCodec
	}
}

func (d *Decoder) reserve(ctx context.Context, n int64) (func(), error) {
	if err := d.sem.Acquire(ctx, n); err != nil {
		return nil, err
	}
	return func() { d.sem.Release(n) }, nil
}

func (d *Decoder) decodeZstd(frame []byte, size int) ([]byte, error) {
	zd := <-d.zstd
	defer func() { d.zstd <- zd }()
	out, err := zd.DecodeAll(frame, make([]byte, 0, size))
	switch {
	case errors.Is(err, zstd.ErrDecoderSizeExceeded), errors.Is(err, zstd.ErrWindowSizeExceeded):
		return nil, tooLarge("zstd output exceeds its declared %d bytes", size)
	case err != nil:
		return nil, malformed("zstd: %v", err)
	}
	if len(out) != size {
		return nil, malformed("zstd output of %d bytes does not match declared %d", len(out), size)
	}
	return out, nil
}

// DecodeZstd decompresses a stored zstd frame (for example a clip) under the same frame-header
// rules as codec 0x01, with max as the bound for the content size.
func (d *Decoder) DecodeZstd(ctx context.Context, frame []byte, max int) ([]byte, error) {
	size, err := checkZstdFrame(frame, max)
	if err != nil {
		return nil, err
	}
	release, err := d.reserve(ctx, int64(size))
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := d.decodeZstd(frame, size)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// unmarshal pre-scans, unmarshals and validates a raw ClientFrame.
func (d *Decoder) unmarshal(raw []byte) (*pb.ClientFrame, error) {
	if err := prescan(raw, d.lim); err != nil {
		return nil, err
	}
	f := &pb.ClientFrame{}
	if err := proto.Unmarshal(raw, f); err != nil {
		fixed, changed, serr := sanitizeUTF8(f.ProtoReflect().Descriptor(), raw, d.lim.MaxDecompressed)
		if serr != nil {
			return nil, serr
		}
		if !changed {
			return nil, malformed("%v", err)
		}
		f.Reset()
		if err := proto.Unmarshal(fixed, f); err != nil {
			return nil, malformed("%v", err)
		}
	}
	if f.Kind == nil {
		return nil, malformed("empty frame")
	}
	if err := validateClientFrame(f); err != nil {
		return nil, err
	}
	return f, nil
}
