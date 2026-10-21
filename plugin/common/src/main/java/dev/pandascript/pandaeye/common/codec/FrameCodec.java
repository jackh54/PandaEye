package dev.pandascript.pandaeye.common.codec;

import dev.pandascript.pandaeye.common.Log;

import java.util.Arrays;
import java.util.function.Supplier;

/**
 * Wire frame codec: byte 0 is the codec ({@code 0x00} raw, {@code 0x01} zstd with content size, {@code 0x02}
 * raw DEFLATE), the rest is the protobuf message. Messages above 1 KiB are compressed with zstd; if
 * aircompressor fails to initialise or throws while compressing, the codec switches to DEFLATE for good and
 * says so once. Decoding enforces the protocol limits before allocating.
 */
public final class FrameCodec {

    public static final byte CODEC_RAW = 0x00;
    public static final byte CODEC_ZSTD = 0x01;
    public static final byte CODEC_DEFLATE = 0x02;
    public static final int MAX_COMPRESSED_BYTES = 1 << 20;
    public static final int MAX_DECOMPRESSED_BYTES = 8 << 20;
    public static final int COMPRESS_THRESHOLD_BYTES = 1024;

    private static final byte[] SELF_TEST = "pandaeye-zstd-self-test-pandaeye-zstd-self-test".getBytes(
            java.nio.charset.StandardCharsets.US_ASCII);

    private final Log log;
    private final Compression deflate = new DeflateCompression();
    private volatile Compression zstd;

    public FrameCodec(Log log) {
        this(log, ZstdCompression::new);
    }

    FrameCodec(Log log, Supplier<Compression> zstdFactory) {
        this.log = log;
        Compression candidate;
        try {
            candidate = zstdFactory.get();
            byte[] frame = candidate.compressFrame(SELF_TEST);
            if (!Arrays.equals(SELF_TEST, candidate.decompress(frame, 1, frame.length - 1, SELF_TEST.length))) {
                throw new IllegalStateException("zstd self test produced different bytes");
            }
        } catch (Throwable t) {
            log.warn("zstd codec unavailable, compressing with DEFLATE instead: " + t);
            candidate = null;
        }
        this.zstd = candidate;
    }

    /** Encodes a message, compressing it when it is larger than {@link #COMPRESS_THRESHOLD_BYTES}. */
    public byte[] encode(byte[] message) {
        if (message.length <= COMPRESS_THRESHOLD_BYTES) {
            return encodeRaw(message);
        }
        Compression active = zstd;
        if (active != null) {
            try {
                return active.compressFrame(message);
            } catch (Throwable t) {
                disableZstd(t);
            }
        }
        return deflate.compressFrame(message);
    }

    /** Encodes a message uncompressed (codec 0x00), as required for Hello. */
    public byte[] encodeRaw(byte[] message) {
        byte[] frame = new byte[message.length + 1];
        frame[0] = CODEC_RAW;
        System.arraycopy(message, 0, frame, 1, message.length);
        return frame;
    }

    /** Encodes with an explicit codec regardless of size (fixtures and tests). */
    byte[] encodeWith(byte codec, byte[] message) {
        return switch (codec) {
            case CODEC_RAW -> encodeRaw(message);
            case CODEC_ZSTD -> requireZstd().compressFrame(message);
            case CODEC_DEFLATE -> deflate.compressFrame(message);
            default -> throw new IllegalArgumentException("unknown codec " + codec);
        };
    }

    public byte[] decode(byte[] frame) throws FrameException {
        return decode(frame, 0, frame.length);
    }

    /** Decodes one frame into the protobuf message bytes, enforcing every size limit. */
    public byte[] decode(byte[] frame, int offset, int length) throws FrameException {
        if (length < 1) {
            throw new FrameException("empty frame");
        }
        if (length - 1 > MAX_COMPRESSED_BYTES) {
            throw new FrameException("frame of " + length + " bytes exceeds " + MAX_COMPRESSED_BYTES);
        }
        byte codec = frame[offset];
        return switch (codec) {
            case CODEC_RAW -> Arrays.copyOfRange(frame, offset + 1, offset + length);
            case CODEC_ZSTD -> decodeZstd(frame, offset + 1, length - 1);
            case CODEC_DEFLATE -> deflate.decompress(frame, offset + 1, length - 1, MAX_DECOMPRESSED_BYTES);
            default -> throw new FrameException("unknown codec 0x" + Integer.toHexString(codec & 0xff));
        };
    }

    /** The codec used for compressed frames: {@link #CODEC_ZSTD} or, after a failure, {@link #CODEC_DEFLATE}. */
    public byte compressionCodec() {
        return zstd != null ? CODEC_ZSTD : CODEC_DEFLATE;
    }

    private byte[] decodeZstd(byte[] frame, int offset, int length) throws FrameException {
        Compression active = zstd;
        if (active == null) {
            throw new FrameException("zstd codec unavailable");
        }
        try {
            return active.decompress(frame, offset, length, MAX_DECOMPRESSED_BYTES);
        } catch (FrameException e) {
            throw e;
        } catch (Throwable t) {
            disableZstd(t);
            throw new FrameException("zstd codec failed", t);
        }
    }

    private Compression requireZstd() {
        Compression active = zstd;
        if (active == null) {
            throw new IllegalStateException("zstd codec unavailable");
        }
        return active;
    }

    private void disableZstd(Throwable cause) {
        synchronized (this) {
            if (zstd == null) {
                return;
            }
            zstd = null;
        }
        log.warn("zstd codec failed, switching to DEFLATE permanently: " + cause);
    }
}
