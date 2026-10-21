package dev.pandascript.pandaeye.common.codec;

import io.airlift.compress.zstd.ZstdCompressor;
import io.airlift.compress.zstd.ZstdDecompressor;

import java.util.Arrays;

/** zstd via the pure-Java aircompressor implementation; every frame carries its content size. */
final class ZstdCompression implements Compression {

    private final ZstdCompressor compressor = new ZstdCompressor();
    private final ZstdDecompressor decompressor = new ZstdDecompressor();

    @Override
    public byte codec() {
        return FrameCodec.CODEC_ZSTD;
    }

    @Override
    public synchronized byte[] compressFrame(byte[] message) {
        int max = compressor.maxCompressedLength(message.length);
        byte[] out = new byte[1 + max];
        out[0] = FrameCodec.CODEC_ZSTD;
        int written = compressor.compress(message, 0, message.length, out, 1, max);
        return Arrays.copyOf(out, 1 + written);
    }

    @Override
    public synchronized byte[] decompress(byte[] source, int offset, int length, int maxBytes) throws FrameException {
        long size;
        try {
            size = ZstdDecompressor.getDecompressedSize(source, offset, length);
        } catch (RuntimeException e) {
            throw new FrameException("invalid zstd frame header", e);
        }
        if (size < 0) {
            throw new FrameException("zstd frame without content size");
        }
        if (size > maxBytes) {
            throw new FrameException("zstd frame declares " + size + " bytes, limit " + maxBytes);
        }
        byte[] out = new byte[(int) size];
        int written;
        try {
            written = decompressor.decompress(source, offset, length, out, 0, out.length);
        } catch (RuntimeException e) {
            throw new FrameException("corrupt zstd frame", e);
        }
        if (written != size) {
            throw new FrameException("zstd frame expanded to " + written + " bytes, declared " + size);
        }
        return out;
    }
}
