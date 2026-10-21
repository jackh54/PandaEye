package dev.pandascript.pandaeye.common.transport;

import org.junit.jupiter.api.Test;

import java.util.random.RandomGenerator;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class BackoffTest {

    @Test
    void doublesFromOneSecondToSixtySecondsWithTwentyPercentJitter() {
        Backoff backoff = new Backoff(1_000, 60_000, RandomGenerator.getDefault());
        long[] nominal = {1_000, 2_000, 4_000, 8_000, 16_000, 32_000, 60_000, 60_000, 60_000};
        for (long expected : nominal) {
            long delay = backoff.nextDelayMillis();
            assertTrue(delay >= expected * 0.8 - 1 && delay <= expected * 1.2 + 1, delay + " vs " + expected);
        }
        backoff.reset();
        long first = backoff.nextDelayMillis();
        assertTrue(first >= 799 && first <= 1_201);
    }

    @Test
    void jitterSpansTheWholeRange() {
        assertEquals(800, new Backoff(1_000, 60_000, fixed(0.0)).nextDelayMillis());
        assertEquals(1_200, new Backoff(1_000, 60_000, fixed(1.0)).nextDelayMillis());
    }

    @Test
    void neverOverflows() {
        Backoff backoff = new Backoff(1_000, 60_000, fixed(0.5));
        for (int i = 0; i < 100; i++) {
            assertEquals(i < 6 ? 1_000L << i : 60_000, backoff.nextDelayMillis());
        }
    }

    private static RandomGenerator fixed(double value) {
        return new RandomGenerator() {
            @Override
            public long nextLong() {
                return 0;
            }

            @Override
            public double nextDouble() {
                return value;
            }
        };
    }
}
