package dev.pandascript.pandaeye.common.codec;

import java.util.Arrays;
import java.util.zip.DataFormatException;
import java.util.zip.Deflater;
import java.util.zip.Inflater;

/** Raw DEFLATE (RFC 1951, no zlib header), the fallback when zstd is unavailable. */
final class DeflateCompression implements Compression {

    @Override
    public byte codec() {
        return FrameCodec.CODEC_DEFLATE;
    }

    @Override
    public byte[] compressFrame(byte[] message) {
        Deflater deflater = new Deflater(Deflater.DEFAULT_COMPRESSION, true);
        try {
            deflater.setInput(message);
            deflater.finish();
            byte[] out = new byte[1 + Math.max(64, message.length / 2)];
            out[0] = FrameCodec.CODEC_DEFLATE;
            int position = 1;
            while (!deflater.finished()) {
                if (position == out.length) {
                    out = Arrays.copyOf(out, out.length * 2);
                }
                position += deflater.deflate(out, position, out.length - position);
            }
            return Arrays.copyOf(out, position);
        } finally {
            deflater.end();
        }
    }

    @Override
    public byte[] decompress(byte[] source, int offset, int length, int maxBytes) throws FrameException {
        Inflater inflater = new Inflater(true);
        try {
            inflater.setInput(source, offset, length);
            byte[] out = new byte[Math.min(maxBytes + 1, Math.max(4096, length * 4))];
            int position = 0;
            boolean paddingFed = false;
            while (!inflater.finished()) {
                if (position == out.length) {
                    if (out.length > maxBytes) {
                        throw new FrameException("DEFLATE payload expands beyond " + maxBytes + " bytes");
                    }
                    out = Arrays.copyOf(out, (int) Math.min(maxBytes + 1L, out.length * 2L));
                }
                int produced = inflater.inflate(out, position, out.length - position);
                position += produced;
                if (produced == 0 && !inflater.finished()) {
                    if (inflater.needsDictionary()) {
                        throw new FrameException("DEFLATE payload requires a dictionary");
                    }
                    if (inflater.needsInput()) {
                        if (paddingFed) {
                            throw new FrameException("truncated DEFLATE payload");
                        }
                        // nowrap inflaters may need one byte beyond the stream to finish.
                        inflater.setInput(new byte[1]);
                        paddingFed = true;
                    }
                }
            }
            if (position > maxBytes) {
                throw new FrameException("DEFLATE payload expands beyond " + maxBytes + " bytes");
            }
            return Arrays.copyOf(out, position);
        } catch (DataFormatException e) {
            throw new FrameException("corrupt DEFLATE payload", e);
        } finally {
            inflater.end();
        }
    }
}
