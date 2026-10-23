package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.paper.state.ClientInfo;
import org.junit.jupiter.api.Test;

import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SessionTest {

    private static final long SECOND = 1_000_000_000L;
    /** A positive nanoTime, as on Linux: {@code now - Long.MIN_VALUE} overflows to a negative number there. */
    private static final long T0 = 5_000 * SECOND;

    private static Session session() {
        return new Session(null, UUID.randomUUID(), "Steve", false, new ClientInfo(774, "1.21.11", true, true, true),
                new SessionSignals(false, () -> null, (from, to) -> false), T0);
    }

    @Test
    void aNewSessionIsNotWatchedAndHasNoRecentVelocity() {
        // Seen live: the unset sentinel read as "watched" and "velocity in the last 2 s" for every player, so
        // everyone streamed full-rate telemetry and every local movement flag was discarded at confirmation.
        Session session = session();
        assertFalse(session.watched(T0));
        assertFalse(session.watched(T0 + 3_600 * SECOND));
        assertFalse(session.bukkitVelocityWithin(T0, 2 * SECOND));
        assertFalse(session.bukkitVelocityWithin(T0 + 3_600 * SECOND, 2 * SECOND));
    }

    @Test
    void aWatchRequestCoversItsWindowOnly() {
        Session session = session();
        session.watchUntil(T0 + 60 * SECOND);
        assertTrue(session.watched(T0 + 59 * SECOND));
        assertFalse(session.watched(T0 + 60 * SECOND));
    }

    @Test
    void aServerVelocityIsRecentForTheWindowOnly() {
        Session session = session();
        session.bukkitVelocity(0, 0.4, 0, T0);
        assertTrue(session.bukkitVelocityWithin(T0 + SECOND, 2 * SECOND));
        assertFalse(session.bukkitVelocityWithin(T0 + 2 * SECOND, 2 * SECOND));
    }
}
