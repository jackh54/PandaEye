package dev.pandascript.pandaeye.common.util;

/**
 * A token bucket on the nanoTime scale. Not thread-safe unless the caller synchronises; see
 * {@link #tryAcquire(long, double)} for the debt semantics.
 */
public final class TokenBucket {

    private double ratePerSecond;
    private double capacity;
    private double tokens;
    private long lastRefillNanos;

    public TokenBucket(double ratePerSecond, double capacity, long nowNanos) {
        configure(ratePerSecond, capacity);
        this.tokens = capacity;
        this.lastRefillNanos = nowNanos;
    }

    public void configure(double ratePerSecond, double capacity) {
        this.ratePerSecond = Math.max(0, ratePerSecond);
        this.capacity = Math.max(1, capacity);
        this.tokens = Math.min(tokens, this.capacity);
    }

    /**
     * Takes {@code cost} tokens. A cost larger than the capacity is admitted once the bucket is full and leaves
     * the bucket in debt, so large requests are delayed rather than starved.
     */
    public boolean tryAcquire(long nowNanos, double cost) {
        refill(nowNanos);
        if (tokens >= Math.min(cost, capacity)) {
            tokens -= cost;
            return true;
        }
        return false;
    }

    public double available(long nowNanos) {
        refill(nowNanos);
        return tokens;
    }

    private void refill(long nowNanos) {
        long elapsed = nowNanos - lastRefillNanos;
        if (elapsed > 0) {
            tokens = Math.min(capacity, tokens + elapsed * ratePerSecond / 1e9);
            lastRefillNanos = nowNanos;
        }
    }
}
