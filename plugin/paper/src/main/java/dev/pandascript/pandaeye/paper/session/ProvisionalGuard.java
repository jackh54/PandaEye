package dev.pandascript.pandaeye.paper.session;

import java.util.ArrayDeque;

/**
 * Bars a player from provisional riptide/elytra exemptions for 10 minutes after 3 failed validations within
 * 60 s (CHECKS 1.1). Thread-safe: failures are reported from the entity thread, read on the netty thread.
 */
final class ProvisionalGuard {

    private static final long WINDOW_NANOS = 60_000_000_000L;
    private static final long BAR_NANOS = 600_000_000_000L;
    private static final int MAX_FAILURES = 3;

    private final ArrayDeque<Long> failures = new ArrayDeque<>();
    private volatile long barredUntil;
    private volatile boolean barred;

    synchronized void failed(long nowNanos) {
        failures.addLast(nowNanos);
        while (!failures.isEmpty() && nowNanos - failures.peekFirst() > WINDOW_NANOS) {
            failures.pollFirst();
        }
        if (failures.size() >= MAX_FAILURES) {
            failures.clear();
            barredUntil = nowNanos + BAR_NANOS;
            barred = true;
        }
    }

    boolean allowed(long nowNanos) {
        return !barred || nowNanos - barredUntil >= 0;
    }
}
