package dev.pandascript.pandaeye.paper.packet;

import java.util.concurrent.atomic.LongAdder;

/**
 * Packet-handler cost histogram (log2 buckets with 4 sub-buckets) for {@code /pandaeye status}: average and
 * p99 microseconds per handled packet since the last reset. Netty threads record into a per-session
 * {@link Recorder} with plain fields and flush it into the shared striped counters every
 * {@value #FLUSH_EVERY} packets, so the packet path does no shared atomic operation.
 */
public final class PacketCost {

    private static final int BUCKETS = 160;
    static final int FLUSH_EVERY = 64;

    private final LongAdder[] buckets = new LongAdder[BUCKETS];
    private final LongAdder count = new LongAdder();
    private final LongAdder totalNanos = new LongAdder();

    public PacketCost() {
        for (int i = 0; i < BUCKETS; i++) {
            buckets[i] = new LongAdder();
        }
    }

    /** Single-thread recorder (one per session, used on its netty thread only). */
    public final class Recorder {

        private final int[] local = new int[BUCKETS];
        private int pending;
        private long pendingNanos;

        public void record(long nanos) {
            local[bucket(nanos)]++;
            pendingNanos += nanos;
            if (++pending >= FLUSH_EVERY) {
                flush();
            }
        }

        public void flush() {
            if (pending == 0) {
                return;
            }
            for (int i = 0; i < BUCKETS; i++) {
                if (local[i] != 0) {
                    buckets[i].add(local[i]);
                    local[i] = 0;
                }
            }
            count.add(pending);
            totalNanos.add(pendingNanos);
            pending = 0;
            pendingNanos = 0;
        }
    }

    public Recorder recorder() {
        return new Recorder();
    }

    /** Direct recording for rare events (JOIN_GAME). */
    public void record(long nanos) {
        buckets[bucket(nanos)].increment();
        count.increment();
        totalNanos.add(nanos);
    }

    private static int bucket(long nanos) {
        long value = Math.max(1, nanos);
        int log = 63 - Long.numberOfLeadingZeros(value);
        int sub = log >= 2 ? (int) ((value >> (log - 2)) & 3) : 0;
        return Math.min(BUCKETS - 1, log * 4 + sub);
    }

    public record Summary(long packets, double averageMicros, double p99Micros) {
    }

    public Summary summary() {
        long packets = count.sum();
        if (packets == 0) {
            return new Summary(0, 0, 0);
        }
        long rank = (long) Math.ceil(packets * 0.99);
        long seen = 0;
        double p99 = 0;
        for (int i = 0; i < BUCKETS; i++) {
            seen += buckets[i].sum();
            if (seen >= rank) {
                int log = i / 4;
                int sub = i % 4;
                double upper = log >= 2 ? (4 + sub + 1) * Math.pow(2, log - 2) : Math.pow(2, log + 1);
                p99 = upper / 1_000.0;
                break;
            }
        }
        return new Summary(packets, totalNanos.sum() / 1_000.0 / packets, p99);
    }

    public void reset() {
        for (LongAdder bucket : buckets) {
            bucket.reset();
        }
        count.reset();
        totalNanos.reset();
    }
}
