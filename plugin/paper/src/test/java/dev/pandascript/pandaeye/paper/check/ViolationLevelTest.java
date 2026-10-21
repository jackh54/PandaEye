package dev.pandascript.pandaeye.paper.check;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ViolationLevelTest {

    private static final long SECOND = 1_000_000_000L;

    @Test
    void decaysLinearlyAndNeverBelowZero() {
        long t = -5 * SECOND; // nanoTime may be negative
        ViolationLevel vl = new ViolationLevel(30);
        assertEquals(1, vl.add(1, t), 1e-9);
        assertEquals(2, vl.add(1, t), 1e-9);
        assertEquals(1.5, vl.value(t + 15 * SECOND), 1e-9);
        assertEquals(0, vl.value(t + 600 * SECOND), 1e-9);
        assertEquals(1, vl.add(1, t + 601 * SECOND), 1e-9);
    }

    @Test
    void anOlderTimestampFromAnotherThreadDoesNotRaiseTheLevel() {
        ViolationLevel vl = new ViolationLevel(10);
        vl.add(3, 100 * SECOND);
        assertEquals(3, vl.value(99 * SECOND), 1e-9);
        assertEquals(4, vl.add(1, 98 * SECOND), 1e-9);
        assertEquals(3.5, vl.value(105 * SECOND), 1e-9);
    }

    @Test
    void zeroDecayKeepsTheLevel() {
        ViolationLevel vl = new ViolationLevel(0);
        vl.add(2, 0);
        assertEquals(2, vl.value(3_600 * SECOND), 1e-9);
    }
}
