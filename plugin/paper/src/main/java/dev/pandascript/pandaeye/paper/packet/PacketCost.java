package dev.pandascript.pandaeye.paper.packet;

import java.util.concurrent.atomic.AtomicLongArray;
import java.util.concurrent.atomic.LongAdder;

/**
 * Packet-handler cost histogram (log2 buckets with 4 sub-buckets) for {@code /pandaeye status}: average and
 * p99 microseconds per handled packet since the last reset. Lock-free.
 */
public final class PacketCost {

    private static final int BUCKETS = 160;

    private final AtomicLongArray buckets = new AtomicLongArray(BUCKETS);
    private final LongAdder count = new LongAdder();
    private final LongAdder totalNanos = new LongAdder();

    public void record(long nanos) {
        long value = Math.max(1, nanos);
        int log = 63 - Long.numberOfLeadingZeros(value);
        int sub = log >= 2 ? (int) ((value >> (log - 2)) & 3) : 0;
        buckets.incrementAndGet(Math.min(BUCKETS - 1, log * 4 + sub));
        count.increment();
        totalNanos.add(nanos);
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
            seen += buckets.get(i);
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
        for (int i = 0; i < BUCKETS; i++) {
            buckets.set(i, 0);
        }
        count.reset();
        totalNanos.reset();
    }
}
