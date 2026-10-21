package dev.pandascript.pandaeye.paper.enforce;

import java.util.EnumMap;
import java.util.Map;
import java.util.concurrent.atomic.LongAdder;

/** Calls per enforcement action (attempted in shadow mode, executed in live mode), for /pandaeye status. */
public final class EnforcerCounters {

    public enum Action {
        SETBACK,
        KICK,
        COMMAND,
        CANCEL,
        DAMAGE,
        VELOCITY,
        LISTENERS
    }

    private final Map<Action, LongAdder> counts = new EnumMap<>(Action.class);

    public EnforcerCounters() {
        for (Action action : Action.values()) {
            counts.put(action, new LongAdder());
        }
    }

    void count(Action action) {
        counts.get(action).increment();
    }

    public long get(Action action) {
        return counts.get(action).sum();
    }

    public long total() {
        long total = 0;
        for (LongAdder adder : counts.values()) {
            total += adder.sum();
        }
        return total;
    }
}
