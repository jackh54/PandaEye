package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayDeque;
import java.util.Iterator;

/**
 * Pending velocities (CHECKS 1.1): own ENTITY_VELOCITY, EXPLOSION knockback and Bukkit PlayerVelocityEvents.
 * A velocity stays pending until a movement matches it, or until it is applied by the client plus 2
 * movement packets, with a 5 s fallback. Netty thread only.
 */
public final class VelocityTracker {

    public enum Source {
        PACKET,
        EXPLOSION,
        BUKKIT
    }

    static final long FALLBACK_NANOS = 5_000_000_000L;
    private static final double MATCH_EPSILON = 0.012;
    private static final int MAX_PENDING = 32;

    private static final class Pending {
        final double vx;
        final double vy;
        final double vz;
        final long sentNanos;
        final Source source;
        int movesAfterApplied;

        Pending(double vx, double vy, double vz, long sentNanos, Source source) {
            this.vx = vx;
            this.vy = vy;
            this.vz = vz;
            this.sentNanos = sentNanos;
            this.source = source;
        }
    }

    private final ArrayDeque<Pending> pending = new ArrayDeque<>();
    private long lastAddedNanos;
    private boolean anyAdded;

    void add(double vx, double vy, double vz, long nanos, Source source) {
        if (pending.size() == MAX_PENDING) {
            pending.pollFirst();
        }
        pending.addLast(new Pending(vx, vy, vz, nanos, source));
        lastAddedNanos = nanos;
        anyAdded = true;
    }

    /** Updates every pending velocity with one movement packet (vertical displacement {@code dy}). */
    void onMove(boolean hasPosition, double dy, AckClock clock, long nowNanos) {
        Iterator<Pending> it = pending.iterator();
        while (it.hasNext()) {
            Pending velocity = it.next();
            boolean applied = clock.applied(velocity.sentNanos);
            if (applied) {
                velocity.movesAfterApplied++;
            }
            boolean matched = hasPosition && Math.abs(velocity.vy) > 0.003 && Math.abs(dy - velocity.vy) < MATCH_EPSILON;
            if (matched || velocity.movesAfterApplied > 2 || nowNanos - velocity.sentNanos >= FALLBACK_NANOS) {
                it.remove();
            }
        }
    }

    void clear() {
        pending.clear();
    }

    public boolean pending() {
        return !pending.isEmpty();
    }

    /** Send time of the most recent velocity of any source; only meaningful when {@link #anyAdded()}. */
    public long lastAddedNanos() {
        return lastAddedNanos;
    }

    public boolean anyAdded() {
        return anyAdded;
    }
}
