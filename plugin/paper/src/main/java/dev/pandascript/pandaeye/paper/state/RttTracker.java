package dev.pandascript.pandaeye.paper.state;

import java.util.Arrays;

/**
 * Round-trip estimates from passively observed keep-alives and pings (ARCHITECTURE 4.3): {@code ping} is the
 * median of the last 5 pong RTTs within 30 s, else the last keep-alive RTT, else 100 ms; {@code rttHigh} is
 * the maximum of the last 5 samples of either kind. Netty thread only.
 */
public final class RttTracker {

    static final long DEFAULT_PING_MILLIS = 100;
    private static final int SAMPLES = 5;
    private static final long PONG_WINDOW_NANOS = 30_000_000_000L;

    private final long[] pongRtt = new long[SAMPLES];
    private final long[] pongAt = new long[SAMPLES];
    private int pongCount;
    private int pongNext;
    private final long[] recent = new long[SAMPLES];
    private int recentCount;
    private int recentNext;
    private long lastKeepAliveNanos = -1;

    void pong(long rttNanos, long nowNanos) {
        pongRtt[pongNext] = rttNanos;
        pongAt[pongNext] = nowNanos;
        pongNext = (pongNext + 1) % SAMPLES;
        pongCount = Math.min(SAMPLES, pongCount + 1);
        record(rttNanos);
    }

    void keepAlive(long rttNanos) {
        lastKeepAliveNanos = rttNanos;
        record(rttNanos);
    }

    public long pingMillis(long nowNanos) {
        long[] fresh = new long[SAMPLES];
        int n = 0;
        for (int i = 0; i < pongCount; i++) {
            if (nowNanos - pongAt[i] <= PONG_WINDOW_NANOS) {
                fresh[n++] = pongRtt[i];
            }
        }
        if (n > 0) {
            long[] sorted = Arrays.copyOf(fresh, n);
            Arrays.sort(sorted);
            return sorted[n / 2] / 1_000_000L;
        }
        if (lastKeepAliveNanos >= 0) {
            return lastKeepAliveNanos / 1_000_000L;
        }
        return DEFAULT_PING_MILLIS;
    }

    public long rttHighMillis(long nowNanos) {
        if (recentCount == 0) {
            return pingMillis(nowNanos);
        }
        long max = 0;
        for (int i = 0; i < recentCount; i++) {
            max = Math.max(max, recent[i]);
        }
        return max / 1_000_000L;
    }

    private void record(long rttNanos) {
        recent[recentNext] = rttNanos;
        recentNext = (recentNext + 1) % SAMPLES;
        recentCount = Math.min(SAMPLES, recentCount + 1);
    }
}
