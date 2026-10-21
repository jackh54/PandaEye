// Package codec decodes and encodes WebSocket frames of the wire protocol (wire.proto header):
// byte 0 selects the codec (0x00 raw protobuf, 0x01 one zstd frame with content size, 0x02 raw
// DEFLATE), the rest is a ClientFrame or ServerFrame.
//
// Decoding is hostile-input safe (ARCHITECTURE 5.3): compressed and decompressed sizes are bounded
// before any large allocation, zstd frame headers are checked before decoding, a global semaphore caps
// in-flight decompressed bytes, a protowire pre-scan bounds the number and size of events before
// proto.Unmarshal, and decoded frames are checked against field limits with strings truncated and
// invalid UTF-8 replaced.
package codec

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

// Codec header bytes.
const (
	Raw     byte = 0x00
	Zstd    byte = 0x01
	Deflate byte = 0x02
)

// Protocol limits (ARCHITECTURE 3 and 5.3).
const (
	MaxCompressed           = 1 << 20   // payload after the codec byte
	MaxDecompressed         = 8 << 20   // decoded protobuf message
	MaxHello                = 16 << 10  // Hello payload, always codec Raw
	MaxEventBytes           = 64 << 10  // one Batch.events element
	DefaultMaxBatchEvents   = 4096      // Welcome.max_batch_events default
	DefaultMaxInFlightBytes = 256 << 20 // global cap on decompressed bytes being decoded

	MaxStringBytes   = 256
	MaxInfoBytes     = 512
	MaxTargetHistory = 64
	MaxDataEntries   = 32
	MaxEffects       = 32
	MaxCapabilities  = 32
	MaxContextMoves  = 40
)

// Errors. Decode errors wrap exactly one of them; the gateway answers ErrTooLarge and ErrMalformed
// batches with Error{CODE_TOO_LARGE} + ack + drop, and closes on ErrUnknownCodec/ErrMalformed
// handshakes.
var (
	ErrUnknownCodec = errors.New("codec: unknown codec")
	ErrTooLarge     = errors.New("codec: frame exceeds limits")
	ErrMalformed    = errors.New("codec: malformed frame")
)

func tooLarge(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrTooLarge}, args...)...)
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrMalformed}, args...)...)
}

// EncodeServerFrame serialises a ServerFrame with the Raw codec (the cloud never compresses).
func EncodeServerFrame(f *pb.ServerFrame) ([]byte, error) {
	return proto.MarshalOptions{}.MarshalAppend([]byte{Raw}, f)
}

// DecodeServerFrame parses a ServerFrame in any codec (plugin side and tests).
func (d *Decoder) DecodeServerFrame(msg []byte) (*pb.ServerFrame, error) {
	raw, release, err := d.payload(context.Background(), msg)
	if err != nil {
		return nil, err
	}
	defer release()
	f := &pb.ServerFrame{}
	if err := proto.Unmarshal(raw, f); err != nil {
		return nil, malformed("%v", err)
	}
	return f, nil
}

// EncodeClientFrame serialises a ClientFrame with the given codec, as a plugin does (tests and
// testkit). Zstd frames carry the content size.
func EncodeClientFrame(f *pb.ClientFrame, codec byte) ([]byte, error) {
	raw, err := proto.Marshal(f)
	if err != nil {
		return nil, err
	}
	return Encode(raw, codec)
}

// Encode prefixes raw with the codec byte, compressing it for Zstd and Deflate.
func Encode(raw []byte, codec byte) ([]byte, error) {
	switch codec {
	case Raw:
		return append([]byte{Raw}, raw...), nil
	case Zstd:
		return append([]byte{Zstd}, CompressZstd(raw)...), nil
	case Deflate:
		out, err := compressDeflate(raw)
		if err != nil {
			return nil, err
		}
		return append([]byte{Deflate}, out...), nil
	default:
		return nil, ErrUnknownCodec
	}
}
