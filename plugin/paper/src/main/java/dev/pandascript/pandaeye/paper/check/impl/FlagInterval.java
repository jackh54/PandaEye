package dev.pandascript.pandaeye.paper.check.impl;

/**
 * At most one flag per interval: a client spamming a forged packet must not turn into a LocalFlag (and a
 * 40-move context) per packet. Netty thread only.
 */
final class FlagInterval {

    static final long SECOND = 1_000_000_000L;

    private final long intervalNanos;
    private long lastNanos;
    private boolean flaggedBefore;

    FlagInterval(long intervalNanos) {
        this.intervalNanos = intervalNanos;
    }

    /** True (and the interval restarts) when a flag may be raised now. */
    boolean admit(long nowNanos) {
        if (flaggedBefore && nowNanos - lastNanos < intervalNanos) {
            return false;
        }
        flaggedBefore = true;
        lastNanos = nowNanos;
        return true;
    }
}
