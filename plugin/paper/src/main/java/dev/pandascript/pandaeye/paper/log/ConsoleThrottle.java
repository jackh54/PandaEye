package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.common.util.TokenBucket;

import java.util.concurrent.ConcurrentHashMap;

/**
 * Alert throttling: per player per check at most one line per 5 s (later lines report how many were
 * suppressed), and at most 20 lines per second overall. Thread-safe.
 */
public final class ConsoleThrottle {

    static final long PER_KEY_NANOS = 5_000_000_000L;
    static final double GLOBAL_PER_SECOND = 20;

    private static final class Slot {
        long lastNanos;
        boolean used;
        int suppressed;
    }

    private final ConcurrentHashMap<String, Slot> slots = new ConcurrentHashMap<>();
    private final TokenBucket global;

    public ConsoleThrottle(long nowNanos) {
        global = new TokenBucket(GLOBAL_PER_SECOND, GLOBAL_PER_SECOND, nowNanos);
    }

    /** Returns -1 when the line must be dropped, else the number of lines suppressed since the last one. */
    public int admit(String key, long nowNanos) {
        Slot slot = slots.computeIfAbsent(key, k -> new Slot());
        synchronized (slot) {
            if (slot.used && nowNanos - slot.lastNanos < PER_KEY_NANOS) {
                slot.suppressed++;
                return -1;
            }
            boolean admitted;
            synchronized (global) {
                admitted = global.tryAcquire(nowNanos, 1);
            }
            if (!admitted) {
                slot.suppressed++;
                return -1;
            }
            slot.used = true;
            slot.lastNanos = nowNanos;
            int suppressed = slot.suppressed;
            slot.suppressed = 0;
            return suppressed;
        }
    }

    public void forget(String keyPrefix) {
        slots.keySet().removeIf(key -> key.startsWith(keyPrefix));
    }
}
