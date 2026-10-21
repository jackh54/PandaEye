package gateway

import (
	"bytes"
	"io"
	"sync"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protowire"
)

// Field numbers in wire.proto read by peekBatchSeq.
const (
	fieldClientFrameBatch = 2
	fieldBatchSeq         = 1
)

// peekLen is how much of a frame peekBatchSeq decodes: the batch's seq is serialised first.
const peekLen = 64

// peekMu serialises zstd peeks, each of which may allocate a decoder window.
var peekMu sync.Mutex

// peekBatchSeq reads Batch.seq from the start of a frame that failed to decode (too many events, an
// oversized event, invalid fields), so the poison batch can be acknowledged. Only the first bytes
// are decompressed, within the protocol's window limit.
func peekBatchSeq(msg []byte) (uint64, bool) {
	if len(msg) < 2 {
		return 0, false
	}
	body := msg[1:]
	buf := make([]byte, peekLen)
	var n int
	switch msg[0] {
	case codec.Raw:
		n = copy(buf, body)
	case codec.Deflate:
		r := flate.NewReader(bytes.NewReader(body))
		n, _ = io.ReadFull(r, buf)
		r.Close()
	case codec.Zstd:
		peekMu.Lock()
		defer peekMu.Unlock()
		d, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxWindow(codec.MaxDecompressed), zstd.WithDecoderMaxMemory(codec.MaxDecompressed))
		if err != nil {
			return 0, false
		}
		n, _ = io.ReadFull(d, buf)
		d.Close()
	default:
		return 0, false
	}
	return parseBatchSeq(buf[:n])
}

// parseBatchSeq parses ClientFrame{batch: Batch{seq ...}} from a prefix of the serialised frame.
func parseBatchSeq(b []byte) (uint64, bool) {
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 || num != fieldClientFrameBatch || typ != protowire.BytesType {
		return 0, false
	}
	b = b[n:]
	if _, n = protowire.ConsumeVarint(b); n < 0 { // the batch's length
		return 0, false
	}
	b = b[n:]
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || typ != protowire.VarintType { // seq, sent_unix_ms and replayed precede the events
			return 0, false
		}
		b = b[n:]
		v, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return 0, false
		}
		if num == fieldBatchSeq {
			return v, true
		}
		b = b[n:]
	}
	return 0, false
}
