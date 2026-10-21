package dev.pandascript.pandaeye.common.transport;

import java.util.Arrays;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.ThreadLocalRandom;

/**
 * Outstanding latency probes and the end-to-end RTT distribution of the last {@value #WINDOW} results.
 * Issued and completed on the cloud thread; the statistics are read from any thread.
 */
final class ProbeTracker {

    static final int WINDOW = 90;
    private static final long EXPIRY_NANOS = 60_000_000_000L;

    private final LinkedHashMap<Long, Long> outstanding = new LinkedHashMap<>();
    private final long[] samples = new long[WINDOW];
    private int count;
    private int next;
    private long nextId = 1 + ThreadLocalRandom.current().nextLong(1L << 40);
    private volatile long lastRttMicros;

    synchronized long issue(long nowNanos) {
        expire(nowNanos);
        long id = nextId++;
        outstanding.put(id, nowNanos);
        return id;
    }

    /** RTT in microseconds, or -1 for an unknown or expired probe id. */
    synchronized long complete(long id, long nowNanos) {
        Long sent = outstanding.remove(id);
        if (sent == null) {
            return -1;
        }
        long rtt = Math.max(0, (nowNanos - sent) / 1_000L);
        samples[next] = rtt;
        next = (next + 1) % WINDOW;
        count = Math.min(WINDOW, count + 1);
        lastRttMicros = rtt;
        return rtt;
    }

    long lastRttMicros() {
        return lastRttMicros;
    }

    /** Nearest-rank percentile of the window in microseconds, 0 when empty. */
    synchronized long percentile(double p) {
        if (count == 0) {
            return 0;
        }
        long[] sorted = Arrays.copyOf(samples, count);
        Arrays.sort(sorted);
        int rank = (int) Math.ceil(p / 100.0 * count);
        return sorted[Math.min(count, Math.max(1, rank)) - 1];
    }

    private void expire(long nowNanos) {
        Iterator<Map.Entry<Long, Long>> it = outstanding.entrySet().iterator();
        while (it.hasNext() && nowNanos - it.next().getValue() > EXPIRY_NANOS) {
            it.remove();
        }
    }
}
