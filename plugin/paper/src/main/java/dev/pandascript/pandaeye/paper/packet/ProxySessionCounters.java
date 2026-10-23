package dev.pandascript.pandaeye.paper.packet;

import dev.pandascript.pandaeye.common.proxy.ProxySessionToken.Rejection;

import java.util.EnumMap;
import java.util.Map;
import java.util.concurrent.atomic.LongAdder;

/**
 * {@code pandaeye:session} messages checked on this server, for /pandaeye status: accepted, and rejected by
 * reason. A wrong or empty secret or clock skew otherwise only shows as "network session none".
 */
public final class ProxySessionCounters {

    private final LongAdder accepted = new LongAdder();
    private final Map<Rejection, LongAdder> rejected = new EnumMap<>(Rejection.class);

    public ProxySessionCounters() {
        for (Rejection reason : Rejection.values()) {
            rejected.put(reason, new LongAdder());
        }
    }

    void countAccepted() {
        accepted.increment();
    }

    void count(Rejection reason) {
        rejected.get(reason).increment();
    }

    public long accepted() {
        return accepted.sum();
    }

    public long rejected(Rejection reason) {
        return rejected.get(reason).sum();
    }

    public long rejectedTotal() {
        long total = 0;
        for (LongAdder adder : rejected.values()) {
            total += adder.sum();
        }
        return total;
    }
}
