package dev.pandascript.pandaeye.paper.telemetry;

import java.util.Iterator;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/** Active cloud WatchRequests: full-rate telemetry for at most 20 players at a time, at most 300 s each. */
public final class WatchRegistry {

    static final int MAX_CONCURRENT = 20;
    static final long MAX_SECONDS = 300;

    private final ConcurrentHashMap<UUID, Long> untilNanos = new ConcurrentHashMap<>();

    /** Returns the watch end ({@code nanoTime} scale), or {@link Long#MIN_VALUE} when the request was refused. */
    public synchronized long watch(UUID player, long seconds, long nowNanos) {
        Iterator<Map.Entry<UUID, Long>> it = untilNanos.entrySet().iterator();
        while (it.hasNext()) {
            if (nowNanos - it.next().getValue() >= 0) {
                it.remove();
            }
        }
        if (seconds <= 0 || (!untilNanos.containsKey(player) && untilNanos.size() >= MAX_CONCURRENT)) {
            return Long.MIN_VALUE;
        }
        long until = nowNanos + Math.min(MAX_SECONDS, seconds) * 1_000_000_000L;
        untilNanos.put(player, until);
        return until;
    }

    public void forget(UUID player) {
        untilNanos.remove(player);
    }

    public int active() {
        return untilNanos.size();
    }
}
