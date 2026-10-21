package dev.pandascript.pandaeye.common.transport;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ProbeTrackerTest {

    @Test
    void measuresRttAndPercentiles() {
        ProbeTracker tracker = new ProbeTracker();
        for (int i = 1; i <= 100; i++) {
            long id = tracker.issue(0);
            assertEquals(i * 1_000L, tracker.complete(id, i * 1_000_000L));
        }
        assertEquals(100_000, tracker.lastRttMicros());
        // the window holds the last 90 samples: 11..100 ms
        assertEquals(55_000, tracker.percentile(50));
        assertEquals(96_000, tracker.percentile(95));
    }

    @Test
    void theReportedRttGoesStaleAfterThirtySeconds() {
        ProbeTracker tracker = new ProbeTracker();
        assertEquals(0, tracker.freshRttMicros(0));
        long id = tracker.issue(0);
        tracker.complete(id, 20_000_000L);
        assertEquals(20_000, tracker.freshRttMicros(25_000_000_000L));
        assertEquals(0, tracker.freshRttMicros(31_000_000_000L), "a broken pipeline reports no data");
    }

    @Test
    void unknownAndExpiredProbesAreIgnored() {
        ProbeTracker tracker = new ProbeTracker();
        assertEquals(-1, tracker.complete(12345, 0));
        long old = tracker.issue(0);
        tracker.issue(61_000_000_000L);
        assertEquals(-1, tracker.complete(old, 61_000_000_000L));
        assertEquals(0, tracker.percentile(50));
    }
}
