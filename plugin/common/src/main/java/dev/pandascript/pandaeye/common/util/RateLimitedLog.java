package dev.pandascript.pandaeye.common.util;

import dev.pandascript.pandaeye.common.Log;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.LongAdder;

/**
 * Logs at most one warning per site per period and counts what it suppressed. Uses only JDK classes and no
 * lambdas, so it is safe on netty threads of a hot-loaded plugin.
 */
public final class RateLimitedLog {

    private final Log log;
    private final long periodNanos;
    private final ConcurrentHashMap<String, Site> sites = new ConcurrentHashMap<>();
    private final LongAdder total = new LongAdder();

    public RateLimitedLog(Log log, long periodNanos) {
        this.log = log;
        this.periodNanos = periodNanos;
    }

    public void warn(String site, String message, Throwable error) {
        total.increment();
        Site state = sites.get(site);
        if (state == null) {
            Site fresh = new Site();
            state = sites.putIfAbsent(site, fresh);
            if (state == null) {
                state = fresh;
            }
        }
        long now = System.nanoTime();
        long suppressed;
        synchronized (state) {
            if (state.logged && now - state.lastLoggedNanos < periodNanos) {
                state.suppressed++;
                return;
            }
            state.logged = true;
            state.lastLoggedNanos = now;
            suppressed = state.suppressed;
            state.suppressed = 0;
        }
        String text = "[" + site + "] " + message + (suppressed > 0 ? " (" + suppressed + " similar suppressed)" : "");
        if (error == null) {
            log.warn(text);
        } else {
            log.warn(text, error);
        }
    }

    /** Number of reported problems since creation, logged or suppressed. */
    public long total() {
        return total.sum();
    }

    private static final class Site {
        boolean logged;
        long lastLoggedNanos;
        long suppressed;
    }
}
