package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayDeque;
import java.util.function.Predicate;
import java.util.function.ToDoubleFunction;

/**
 * A server-controlled value as the client sees it (CHECKS 1.1 "pending state"): the value the client has
 * applied, plus changes the server sent that the client has not provably applied yet. {@code null} means
 * unknown. Checks use the more lenient of all candidate values until a change is applied. Netty thread only.
 */
public final class Tracked<T> {

    private static final int MAX_PENDING = 16;

    private record Change<T>(T value, long sentNanos) {
    }

    private final ArrayDeque<Change<T>> pending = new ArrayDeque<>(4);
    private T applied;

    public Tracked(T initial) {
        this.applied = initial;
    }

    /** Replaces the whole state (join, respawn, hot-load baseline). */
    void reset(T value) {
        applied = value;
        pending.clear();
    }

    /** A change the server has just sent. */
    void sent(T value, long sentNanos) {
        if (pending.size() == MAX_PENDING) {
            applied = pending.pollFirst().value();
        }
        pending.addLast(new Change<>(value, sentNanos));
    }

    /** Moves every change the client has applied into {@link #applied()}. */
    void promote(AckClock clock) {
        while (!pending.isEmpty() && clock.applied(pending.peekFirst().sentNanos())) {
            applied = pending.pollFirst().value();
        }
    }

    public T applied() {
        return applied;
    }

    /** The newest value the server sent (what the client will have once everything is applied). */
    public T latest() {
        return pending.isEmpty() ? applied : pending.peekLast().value();
    }

    public boolean isPending() {
        return !pending.isEmpty();
    }

    /** False when the applied value or any pending value is unknown. */
    public boolean known() {
        if (applied == null) {
            return false;
        }
        for (Change<T> change : pending) {
            if (change.value() == null) {
                return false;
            }
        }
        return true;
    }

    /** True when the applied value or any pending value matches. */
    public boolean anyMatch(Predicate<? super T> predicate) {
        if (applied != null && predicate.test(applied)) {
            return true;
        }
        for (Change<T> change : pending) {
            if (change.value() != null && predicate.test(change.value())) {
                return true;
            }
        }
        return false;
    }

    /** The maximum over applied and pending values; NaN when unknown. */
    public double max(ToDoubleFunction<? super T> value) {
        if (!known()) {
            return Double.NaN;
        }
        double max = value.applyAsDouble(applied);
        for (Change<T> change : pending) {
            max = Math.max(max, value.applyAsDouble(change.value()));
        }
        return max;
    }

    /** The minimum over applied and pending values; NaN when unknown. */
    public double min(ToDoubleFunction<? super T> value) {
        if (!known()) {
            return Double.NaN;
        }
        double min = value.applyAsDouble(applied);
        for (Change<T> change : pending) {
            min = Math.min(min, value.applyAsDouble(change.value()));
        }
        return min;
    }
}
