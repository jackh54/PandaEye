package dev.pandascript.pandaeye.paper.check;

/** A local violation level with linear decay (1 per {@code decaySeconds}). Thread-safe. */
public final class ViolationLevel {

    private final double decayPerNano;
    private double value;
    private long updatedNanos;
    private boolean started;

    public ViolationLevel(double decaySeconds) {
        this.decayPerNano = decaySeconds > 0 ? 1.0 / (decaySeconds * 1e9) : 0;
    }

    public synchronized double add(double increment, long nowNanos) {
        decay(nowNanos);
        value += increment;
        return value;
    }

    public synchronized double value(long nowNanos) {
        decay(nowNanos);
        return value;
    }

    /** Netty and entity threads pass slightly different clocks; time never runs backwards for the decay. */
    private void decay(long nowNanos) {
        if (started && nowNanos - updatedNanos <= 0) {
            return;
        }
        if (started && value > 0) {
            value = Math.max(0, value - (nowNanos - updatedNanos) * decayPerNano);
        }
        started = true;
        updatedNanos = nowNanos;
    }
}
