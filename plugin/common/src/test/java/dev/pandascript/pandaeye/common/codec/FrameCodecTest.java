package dev.pandascript.pandaeye.common.codec;

import dev.pandascript.pandaeye.common.TestLog;
import io.airlift.compress.zstd.ZstdCompressor;
import org.junit.jupiter.api.Test;

import java.nio.ByteBuffer;
import java.util.Arrays;
import java.util.Random;
import java.util.zip.Deflater;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FrameCodecTest {

    private final TestLog log = new TestLog();
    private final FrameCodec codec = new FrameCodec(log);

    @Test
    void smallMessagesStayRaw() throws Exception {
        byte[] message = compressible(FrameCodec.COMPRESS_THRESHOLD_BYTES);
        byte[] frame = codec.encode(message);
        assertEquals(FrameCodec.CODEC_RAW, frame[0]);
        assertArrayEquals(message, codec.decode(frame));
    }

    @Test
    void largerMessagesUseZstdWithContentSize() throws Exception {
        byte[] message = compressible(64 * 1024);
        byte[] frame = codec.encode(message);
        assertEquals(FrameCodec.CODEC_ZSTD, frame[0]);
        assertTrue(frame.length < message.length / 4);
        assertEquals(message.length,
                io.airlift.compress.zstd.ZstdDecompressor.getDecompressedSize(frame, 1, frame.length - 1));
        assertArrayEquals(message, codec.decode(frame));
    }

    @Test
    void deflateRoundTripsIncludingIncompressibleData() throws Exception {
        for (byte[] message : new byte[][]{compressible(100_000), random(50_000), new byte[0]}) {
            byte[] frame = codec.encodeWith(FrameCodec.CODEC_DEFLATE, message);
            assertEquals(FrameCodec.CODEC_DEFLATE, frame[0]);
            assertArrayEquals(message, codec.decode(frame));
        }
    }

    @Test
    void zstdThatFailsToInitialiseFallsBackToDeflate() throws Exception {
        FrameCodec broken = new FrameCodec(log, () -> {
            throw new NoClassDefFoundError("io/airlift/compress/zstd/ZstdCompressor");
        });
        assertEquals(FrameCodec.CODEC_DEFLATE, broken.compressionCodec());
        byte[] message = compressible(10_000);
        byte[] frame = broken.encode(message);
        assertEquals(FrameCodec.CODEC_DEFLATE, frame[0]);
        assertArrayEquals(message, broken.decode(frame));
        assertTrue(log.contains("DEFLATE"));
    }

    @Test
    void zstdThatThrowsInUseFallsBackPermanentlyAndLogsOnce() throws Exception {
        FailingAfterSelfTest flaky = new FailingAfterSelfTest();
        FrameCodec codec = new FrameCodec(log, () -> flaky);
        assertEquals(FrameCodec.CODEC_ZSTD, codec.compressionCodec());
        flaky.fail = true;
        byte[] message = compressible(10_000);
        for (int i = 0; i < 3; i++) {
            byte[] frame = codec.encode(message);
            assertEquals(FrameCodec.CODEC_DEFLATE, frame[0]);
            assertArrayEquals(message, codec.decode(frame));
        }
        assertEquals(FrameCodec.CODEC_DEFLATE, codec.compressionCodec());
        assertEquals(1, log.lines().stream().filter(l -> l.contains("switching to DEFLATE")).count());
        assertEquals(1, flaky.calls - 1, "zstd is not retried after the failure");
    }

    @Test
    void rejectsZstdFramesDeclaringMoreThanEightMiBBeforeAllocating() {
        byte[] header = zstdFrameHeader(FrameCodec.MAX_DECOMPRESSED_BYTES + 1L);
        FrameException e = assertThrows(FrameException.class, () -> codec.decode(header));
        assertTrue(e.getMessage().contains("declares"), e.getMessage());
    }

    @Test
    void rejectsZstdFramesWithoutContentSize() {
        // magic, frame header descriptor 0x00 (no content size, window descriptor follows), window byte.
        byte[] frame = {FrameCodec.CODEC_ZSTD, 0x28, (byte) 0xb5, 0x2f, (byte) 0xfd, 0x00, 0x48, 0x01, 0x00, 0x00};
        FrameException e = assertThrows(FrameException.class, () -> codec.decode(frame));
        assertTrue(e.getMessage().contains("content size") || e.getMessage().contains("corrupt"), e.getMessage());
    }

    @Test
    void rejectsZstdFramesThatExpandBeyondTheirDeclaredSize() {
        byte[] small = compressible(2_000);
        byte[] big = compressible(100_000);
        byte[] first = zstd(small);
        byte[] second = zstd(big);
        // two concatenated frames: the first header declares 2 000 bytes, the payload expands to 102 000.
        byte[] frame = new byte[1 + first.length + second.length];
        frame[0] = FrameCodec.CODEC_ZSTD;
        System.arraycopy(first, 0, frame, 1, first.length);
        System.arraycopy(second, 0, frame, 1 + first.length, second.length);
        assertThrows(FrameException.class, () -> codec.decode(frame));
    }

    @Test
    void rejectsDeflateBombs() {
        byte[] zeros = new byte[FrameCodec.MAX_DECOMPRESSED_BYTES + 1024];
        Deflater deflater = new Deflater(Deflater.BEST_COMPRESSION, true);
        deflater.setInput(zeros);
        deflater.finish();
        byte[] out = new byte[64 * 1024];
        int length = deflater.deflate(out);
        deflater.end();
        assertTrue(length < 20_000);
        byte[] frame = new byte[length + 1];
        frame[0] = FrameCodec.CODEC_DEFLATE;
        System.arraycopy(out, 0, frame, 1, length);
        FrameException e = assertThrows(FrameException.class, () -> codec.decode(frame));
        assertTrue(e.getMessage().contains("expands beyond"), e.getMessage());
    }

    @Test
    void rejectsTruncatedAndCorruptPayloads() {
        byte[] frame = codec.encodeWith(FrameCodec.CODEC_DEFLATE, random(10_000));
        assertThrows(FrameException.class, () -> codec.decode(Arrays.copyOf(frame, frame.length / 2)));
        byte[] zstdFrame = codec.encode(compressible(10_000));
        for (int i = 10; i < zstdFrame.length; i += 7) {
            zstdFrame[i] ^= 0x5a;
        }
        assertThrows(FrameException.class, () -> codec.decode(zstdFrame));
    }

    @Test
    void rejectsOversizedEmptyAndUnknownFrames() {
        assertThrows(FrameException.class, () -> codec.decode(new byte[0]));
        assertThrows(FrameException.class, () -> codec.decode(new byte[]{0x07, 1, 2}));
        byte[] oversized = new byte[FrameCodec.MAX_COMPRESSED_BYTES + 2];
        assertThrows(FrameException.class, () -> codec.decode(oversized));
    }

    static byte[] compressible(int size) {
        byte[] out = new byte[size];
        for (int i = 0; i < size; i++) {
            out[i] = (byte) ("pandaeye-".charAt(i % 9) + (i / 997 % 3));
        }
        return out;
    }

    private static byte[] random(int size) {
        byte[] out = new byte[size];
        new Random(42).nextBytes(out);
        return out;
    }

    private static byte[] zstd(byte[] message) {
        ZstdCompressor compressor = new ZstdCompressor();
        byte[] out = new byte[compressor.maxCompressedLength(message.length)];
        int n = compressor.compress(message, 0, message.length, out, 0, out.length);
        return Arrays.copyOf(out, n);
    }

    /** A zstd frame header (single segment, 8-byte content size) followed by a raw empty last block. */
    private static byte[] zstdFrameHeader(long contentSize) {
        ByteBuffer buffer = ByteBuffer.allocate(1 + 4 + 1 + 8 + 3).order(java.nio.ByteOrder.LITTLE_ENDIAN);
        buffer.put(FrameCodec.CODEC_ZSTD).putInt(0xFD2FB528).put((byte) 0xE0).putLong(contentSize);
        buffer.put((byte) 0x01).put((byte) 0).put((byte) 0);
        return buffer.array();
    }

    private static final class FailingAfterSelfTest implements Compression {
        private final ZstdCompression delegate = new ZstdCompression();
        volatile boolean fail;
        int calls;

        @Override
        public byte codec() {
            return FrameCodec.CODEC_ZSTD;
        }

        @Override
        public byte[] compressFrame(byte[] message) {
            calls++;
            if (fail) {
                throw new IllegalStateException("simulated aircompressor failure");
            }
            return delegate.compressFrame(message);
        }

        @Override
        public byte[] decompress(byte[] source, int offset, int length, int maxBytes) throws FrameException {
            return delegate.decompress(source, offset, length, maxBytes);
        }
    }
}
