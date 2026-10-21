package dev.pandascript.pandaeye.common.verdict;

import java.util.ArrayDeque;
import java.util.Collection;
import java.util.Iterator;

/**
 * The punishment circuit breaker (ARCHITECTURE 4.7): at most {@code maxBansPerHour} bans per rolling hour and
 * {@code maxKicksPer10Min} kicks per rolling 10 minutes. The attempt that would exceed a limit opens the breaker;
 * an open breaker refuses every punishment and mitigation until {@link #reset}, even after the window has
 * rolled over (it latches, so a false-positive wave cannot resume on its own). Thread-safe.
 */
public final class CircuitBreaker {

    public static final long BAN_WINDOW_MILLIS = 3_600_000L;
    public static final long KICK_WINDOW_MILLIS = 600_000L;

    public enum Kind {
        BAN,
        KICK
    }

    public enum Result {
        /** Within the limit: execute. */
        ADMITTED,
        /** This attempt exceeded the limit and opened the breaker: do not execute, alert. */
        TRIPPED,
        /** The breaker was already open: do not execute. */
        OPEN
    }

    public record Status(boolean open, String reason, long openSinceUnixMs, int bansLastHour, int kicksLast10Min,
                         int maxBansPerHour, int maxKicksPer10Min) {
    }

    private final int maxBans;
    private final int maxKicks;
    private final ArrayDeque<Long> bans = new ArrayDeque<>();
    private final ArrayDeque<Long> kicks = new ArrayDeque<>();
    private boolean open;
    private String reason = "";
    private long openSince;

    public CircuitBreaker(int maxBansPerHour, int maxKicksPer10Min) {
        this.maxBans = Math.max(0, maxBansPerHour);
        this.maxKicks = Math.max(0, maxKicksPer10Min);
    }

    /** Counts an attempt; see {@link Result}. */
    public synchronized Result acquire(Kind kind, long nowUnixMs) {
        if (open) {
            return Result.OPEN;
        }
        ArrayDeque<Long> window = kind == Kind.BAN ? bans : kicks;
        long length = kind == Kind.BAN ? BAN_WINDOW_MILLIS : KICK_WINDOW_MILLIS;
        int max = kind == Kind.BAN ? maxBans : maxKicks;
        prune(window, nowUnixMs - length);
        if (window.size() >= max) {
            open = true;
            openSince = nowUnixMs;
            reason = kind == Kind.BAN
                    ? "more than " + max + " bans within an hour"
                    : "more than " + max + " kicks within 10 minutes";
            return Result.TRIPPED;
        }
        window.addLast(nowUnixMs);
        return Result.ADMITTED;
    }

    /** Takes back an admitted attempt that was not executed after all. */
    public synchronized void release(Kind kind, long admittedUnixMs) {
        (kind == Kind.BAN ? bans : kicks).removeLastOccurrence(admittedUnixMs);
    }

    public synchronized boolean isOpen() {
        return open;
    }

    /** Closes the breaker and forgets the counted attempts. */
    public synchronized void reset() {
        open = false;
        reason = "";
        openSince = 0;
        bans.clear();
        kicks.clear();
    }

    /** Restores state persisted by an earlier process: counted executions and a latched open state. */
    public synchronized void restore(Collection<Long> banTimes, Collection<Long> kickTimes, boolean wasOpen,
                                     long sinceUnixMs, String why) {
        bans.clear();
        kicks.clear();
        banTimes.stream().sorted().forEach(bans::addLast);
        kickTimes.stream().sorted().forEach(kicks::addLast);
        open = wasOpen;
        openSince = wasOpen ? sinceUnixMs : 0;
        reason = wasOpen ? (why == null ? "" : why) : "";
    }

    public synchronized Status status(long nowUnixMs) {
        prune(bans, nowUnixMs - BAN_WINDOW_MILLIS);
        prune(kicks, nowUnixMs - KICK_WINDOW_MILLIS);
        return new Status(open, reason, openSince, bans.size(), kicks.size(), maxBans, maxKicks);
    }

    private static void prune(ArrayDeque<Long> window, long cutoff) {
        Iterator<Long> it = window.iterator();
        while (it.hasNext()) {
            if (it.next() <= cutoff) {
                it.remove();
            } else {
                return;
            }
        }
    }
}
