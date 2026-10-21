package dev.pandascript.pandaeye.paper.telemetry;

import org.junit.jupiter.api.Test;

import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;

/** WatchRequest clamping (ARCHITECTURE 4.6, PROTOCOL 8): at most 300 s and at most 20 concurrent watches. */
class WatchRegistryTest {

    private static final long S = 1_000_000_000L;

    @Test
    void clampsTheDurationAndTheNumberOfConcurrentWatches() {
        WatchRegistry watches = new WatchRegistry();
        long now = 1_000 * S;
        assertEquals(now + 300 * S, watches.watch(UUID.randomUUID(), 3_600, now), "clamped to 300 s");
        assertEquals(now + 60 * S, watches.watch(UUID.randomUUID(), 60, now));
        assertEquals(Long.MIN_VALUE, watches.watch(UUID.randomUUID(), 0, now), "0 s is refused");
        for (int i = 2; i < 20; i++) {
            assertNotEquals(Long.MIN_VALUE, watches.watch(UUID.randomUUID(), 30, now));
        }
        assertEquals(20, watches.active());
        assertEquals(Long.MIN_VALUE, watches.watch(UUID.randomUUID(), 30, now), "the 21st concurrent watch is refused");
        assertEquals(Long.MIN_VALUE + 0, watches.watch(UUID.randomUUID(), 300, now + 29 * S));
        assertNotEquals(Long.MIN_VALUE, watches.watch(UUID.randomUUID(), 30, now + 31 * S), "expired watches free slots");
    }

    @Test
    void aWatchedPlayerCanBeExtendedAtTheLimit() {
        WatchRegistry watches = new WatchRegistry();
        UUID first = UUID.randomUUID();
        watches.watch(first, 10, 0);
        for (int i = 1; i < 20; i++) {
            watches.watch(UUID.randomUUID(), 100, 0);
        }
        assertEquals(200 * S, watches.watch(first, 200, 0));
        watches.forget(first);
        assertEquals(19, watches.active());
    }
}
