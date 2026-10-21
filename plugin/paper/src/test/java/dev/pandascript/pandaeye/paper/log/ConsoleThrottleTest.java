package dev.pandascript.pandaeye.paper.log;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ConsoleThrottleTest {

    private static final long MS = 1_000_000L;

    @Test
    void onePerKeyPerFiveSecondsReportingSuppressedLines() {
        ConsoleThrottle throttle = new ConsoleThrottle(0);
        assertEquals(0, throttle.admit("a:speed", 0));
        assertEquals(-1, throttle.admit("a:speed", 1_000 * MS));
        assertEquals(-1, throttle.admit("a:speed", 4_999 * MS));
        assertEquals(0, throttle.admit("b:speed", 4_999 * MS));
        assertEquals(2, throttle.admit("a:speed", 5_000 * MS));
        assertEquals(-1, throttle.admit("a:speed", 5_001 * MS));
    }

    @Test
    void atMostTwentyLinesPerSecondOverall() {
        ConsoleThrottle throttle = new ConsoleThrottle(0);
        int admitted = 0;
        for (int i = 0; i < 100; i++) {
            if (throttle.admit("p" + i + ":timer", 10 * MS) >= 0) {
                admitted++;
            }
        }
        assertEquals(20, admitted);
        // a key that lost to the global limit is not blocked for five seconds and counts its suppressed line
        assertEquals(1, throttle.admit("p99:timer", 1_100 * MS));
    }

    @Test
    void forgetClearsAPlayersKeys() {
        ConsoleThrottle throttle = new ConsoleThrottle(0);
        throttle.admit("a:speed", 0);
        throttle.admit("a:speed", MS);
        throttle.forget("a:");
        assertEquals(0, throttle.admit("a:speed", 2 * MS));
    }
}
