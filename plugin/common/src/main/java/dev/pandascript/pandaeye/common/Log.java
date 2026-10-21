package dev.pandascript.pandaeye.common;

/**
 * Minimal logging facade so platform-independent code never touches a platform logger directly.
 * Implementations must be thread-safe and must never block.
 */
public interface Log {

    void info(String message);

    void warn(String message);

    void warn(String message, Throwable error);

    /** Only emitted when the platform's debug switch is on. */
    void debug(String message);
}
