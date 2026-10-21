package dev.pandascript.pandaeye.paper.packet;

import java.util.concurrent.atomic.LongAdder;

/**
 * The {@code active} flag and in-flight counter every foreign-thread callback goes through (ARCHITECTURE 4.1):
 * {@code if (!gate.enter()) return; try { ... } finally { gate.exit(); }}. Disable closes the gate and waits
 * (bounded) for callbacks already inside.
 */
public final class ActiveGate {

    private final LongAdder inFlight = new LongAdder();
    private volatile boolean active;

    public boolean active() {
        return active;
    }

    public void open() {
        active = true;
    }

    /** Returns false (and enters nothing) when inactive. */
    public boolean enter() {
        if (!active) {
            return false;
        }
        inFlight.increment();
        if (!active) {
            inFlight.decrement();
            return false;
        }
        return true;
    }

    public void exit() {
        inFlight.decrement();
    }

    /** Deactivates and waits up to {@code waitNanos} for in-flight callbacks; returns true if all finished. */
    public boolean close(long waitNanos) {
        active = false;
        long deadline = System.nanoTime() + waitNanos;
        while (inFlight.sum() > 0) {
            if (System.nanoTime() - deadline >= 0) {
                return false;
            }
            Thread.onSpinWait();
            try {
                Thread.sleep(1);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return inFlight.sum() == 0;
            }
        }
        return true;
    }

    public long inFlight() {
        return inFlight.sum();
    }
}
