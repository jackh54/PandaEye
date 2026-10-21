package dev.pandascript.pandaeye.common.codec;

/** A frame that violates the wire limits or cannot be decoded. */
public final class FrameException extends Exception {

    private static final long serialVersionUID = 1L;

    public FrameException(String message) {
        super(message);
    }

    public FrameException(String message, Throwable cause) {
        super(message, cause);
    }
}
