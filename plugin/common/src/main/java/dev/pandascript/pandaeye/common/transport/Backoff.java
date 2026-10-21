package dev.pandascript.pandaeye.common.transport;

import java.util.random.RandomGenerator;

/** Exponential reconnect backoff with +-20% jitter. */
final class Backoff {

    private final long baseMillis;
    private final long maxMillis;
    private final RandomGenerator random;
    private int attempt;

    Backoff(long baseMillis, long maxMillis, RandomGenerator random) {
        this.baseMillis = baseMillis;
        this.maxMillis = maxMillis;
        this.random = random;
    }

    long nextDelayMillis() {
        long delay = baseMillis << Math.min(attempt, 30);
        if (delay <= 0 || delay > maxMillis) {
            delay = maxMillis;
        }
        attempt++;
        double jitter = 0.8 + 0.4 * random.nextDouble();
        return Math.max(1, Math.round(delay * jitter));
    }

    void reset() {
        attempt = 0;
    }

    int attempts() {
        return attempt;
    }
}
