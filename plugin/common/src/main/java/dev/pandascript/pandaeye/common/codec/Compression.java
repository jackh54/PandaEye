package dev.pandascript.pandaeye.common.codec;

/** One compressed wire codec. Implementations are thread-safe. */
interface Compression {

    byte codec();

    /** Returns the codec byte followed by the compressed message. */
    byte[] compressFrame(byte[] message);

    /** Decompresses a payload (without the codec byte) that must expand to at most {@code maxBytes}. */
    byte[] decompress(byte[] source, int offset, int length, int maxBytes) throws FrameException;
}
