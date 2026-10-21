package codec

import (
	"bytes"
	"encoding/binary"
	"io"
	"sync"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/zstd"
)

const (
	zstdMagic        = 0xFD2FB528
	zstdMaxBlockSize = 128 << 10
)

// checkZstdFrame validates the zstd frame header and block layout without decompressing (RFC 8878):
// exactly one frame, no dictionary, Frame_Content_Size present and <= max, window <= max, blocks
// within the input and nothing after the frame. It returns the declared content size.
func checkZstdFrame(b []byte, max int) (int, error) {
	if len(b) < 6 {
		return 0, malformed("zstd frame too short")
	}
	if binary.LittleEndian.Uint32(b) != zstdMagic {
		return 0, malformed("not a zstd frame (skippable or unknown magic)")
	}
	fhd := b[4]
	fcsFlag := fhd >> 6
	single := fhd&0x20 != 0
	if fhd&0x08 != 0 {
		return 0, malformed("zstd reserved bit set")
	}
	checksum := fhd&0x04 != 0
	dictSize := [4]int{0, 1, 2, 4}[fhd&0x03]
	if dictSize != 0 {
		return 0, malformed("zstd dictionaries are not supported")
	}
	pos := 5
	if !single {
		wd := b[pos]
		pos++
		windowLog := 10 + uint(wd>>3)
		window := uint64(1) << windowLog
		window += (window / 8) * uint64(wd&7)
		if window > uint64(max) {
			return 0, tooLarge("zstd window %d exceeds %d", window, max)
		}
	}
	fcsSize := [4]int{0, 2, 4, 8}[fcsFlag]
	if fcsFlag == 0 && single {
		fcsSize = 1
	}
	if fcsSize == 0 {
		return 0, malformed("zstd frame without content size")
	}
	if len(b) < pos+fcsSize {
		return 0, malformed("zstd frame header truncated")
	}
	var size uint64
	switch fcsSize {
	case 1:
		size = uint64(b[pos])
	case 2:
		size = uint64(binary.LittleEndian.Uint16(b[pos:])) + 256
	case 4:
		size = uint64(binary.LittleEndian.Uint32(b[pos:]))
	case 8:
		size = binary.LittleEndian.Uint64(b[pos:])
	}
	pos += fcsSize
	if size > uint64(max) {
		return 0, tooLarge("zstd content size %d exceeds %d", size, max)
	}
	for last := false; !last; {
		if len(b) < pos+3 {
			return 0, malformed("zstd block header truncated")
		}
		bh := uint32(b[pos]) | uint32(b[pos+1])<<8 | uint32(b[pos+2])<<16
		pos += 3
		last = bh&1 != 0
		blockSize := int(bh >> 3)
		if blockSize > zstdMaxBlockSize {
			return 0, malformed("zstd block of %d bytes exceeds the maximum block size", blockSize)
		}
		switch (bh >> 1) & 3 {
		case 0, 2: // raw, compressed
			pos += blockSize
		case 1: // RLE: one byte repeated blockSize times
			pos++
		default:
			return 0, malformed("zstd reserved block type")
		}
		if pos > len(b) {
			return 0, malformed("zstd block truncated")
		}
	}
	if checksum {
		pos += 4
	}
	if pos != len(b) {
		return 0, malformed("zstd input must be exactly one frame")
	}
	return int(size), nil
}

var zstdEncoder = sync.OnceValue(func() *zstd.Encoder {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(false), zstd.WithSingleSegment(true))
	if err != nil {
		panic("codec: zstd encoder: " + err.Error())
	}
	return enc
})

// CompressZstd returns one zstd frame of raw with Frame_Content_Size set (codec 0x01 payloads and
// stored clips, clips.codec = 1).
func CompressZstd(raw []byte) []byte { return zstdEncoder().EncodeAll(raw, nil) }

func compressDeflate(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// inflate decodes a raw DEFLATE stream into at most max bytes. Output is collected in chunks of
// growing size and joined once, so a bomb costs about twice max in allocations, never more.
func inflate(b []byte, max int) ([]byte, error) {
	src := bytes.NewReader(b)
	r := flate.NewReader(src)
	defer r.Close()
	var chunks [][]byte
	total, size := 0, 16<<10
	for done := false; !done; {
		c := make([]byte, size)
		n := 0
		for n < len(c) {
			m, err := r.Read(c[n:])
			n += m
			if err == io.EOF {
				done = true
				break
			}
			if err != nil {
				return nil, malformed("deflate: %v", err)
			}
		}
		if total += n; total > max {
			return nil, tooLarge("deflate output exceeds %d bytes", max)
		}
		chunks = append(chunks, c[:n])
		size = min(2*size, 1<<20)
	}
	if src.Len() != 0 {
		return nil, malformed("data after the end of the deflate stream")
	}
	out := make([]byte, 0, total)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out, nil
}
