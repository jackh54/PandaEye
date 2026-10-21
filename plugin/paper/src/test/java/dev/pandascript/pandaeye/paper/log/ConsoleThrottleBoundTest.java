package dev.pandascript.pandaeye.paper.log;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertTrue;

/** One throttle key per verdict or player must not grow the map without bound (ARCHITECTURE 1.1 rule 7). */
class ConsoleThrottleBoundTest {

    @Test
    void idleKeysAreForgotten() {
        ConsoleThrottle throttle = new ConsoleThrottle(0);
        long now = 0;
        for (int i = 0; i < 50_000; i++) {
            now += 60_000_000L;
            throttle.admit("verdict|" + i, now);
            assertTrue(throttle.size() <= ConsoleThrottle.MAX_SLOTS, "size " + throttle.size());
        }
    }
}
