package dev.pandascript.pandaeye.common.verdict;

import org.junit.jupiter.api.Test;

import java.util.List;

import static dev.pandascript.pandaeye.common.verdict.CircuitBreaker.Kind.BAN;
import static dev.pandascript.pandaeye.common.verdict.CircuitBreaker.Kind.KICK;
import static dev.pandascript.pandaeye.common.verdict.CircuitBreaker.Result.ADMITTED;
import static dev.pandascript.pandaeye.common.verdict.CircuitBreaker.Result.OPEN;
import static dev.pandascript.pandaeye.common.verdict.CircuitBreaker.Result.TRIPPED;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CircuitBreakerTest {

    private static final long T = 1_700_000_000_000L;

    @Test
    void admitsUpToTheLimitThenTripsAndLatches() {
        CircuitBreaker breaker = new CircuitBreaker(3, 5);
        assertEquals(ADMITTED, breaker.acquire(BAN, T));
        assertEquals(ADMITTED, breaker.acquire(BAN, T + 1));
        assertEquals(ADMITTED, breaker.acquire(BAN, T + 2));
        assertEquals(TRIPPED, breaker.acquire(BAN, T + 3));
        assertEquals(OPEN, breaker.acquire(BAN, T + 4));
        assertEquals(OPEN, breaker.acquire(KICK, T + 5), "an open breaker stops kicks too");
        assertEquals(OPEN, breaker.acquire(BAN, T + 10 * 3_600_000L), "it stays open after the window rolls over");
        CircuitBreaker.Status status = breaker.status(T + 4);
        assertTrue(status.open());
        assertEquals(T + 3, status.openSinceUnixMs());
        assertTrue(status.reason().contains("3 bans"), status.reason());
        breaker.reset();
        assertFalse(breaker.isOpen());
        assertEquals(ADMITTED, breaker.acquire(BAN, T + 6));
    }

    @Test
    void windowsRoll() {
        CircuitBreaker breaker = new CircuitBreaker(3, 5);
        for (int i = 0; i < 5; i++) {
            assertEquals(ADMITTED, breaker.acquire(KICK, T + i));
        }
        assertEquals(ADMITTED, breaker.acquire(KICK, T + 600_000L), "the first kick left the 10 min window");
        assertEquals(TRIPPED, breaker.acquire(KICK, T + 600_000L));
        CircuitBreaker bans = new CircuitBreaker(3, 5);
        bans.acquire(BAN, T);
        bans.acquire(BAN, T + 1);
        bans.acquire(BAN, T + 2);
        assertEquals(ADMITTED, bans.acquire(BAN, T + 3_600_001L));
        assertEquals(1, bans.status(T + 3_600_002L).bansLastHour());
    }

    @Test
    void releaseTakesBackAnAttemptThatWasNotExecuted() {
        CircuitBreaker breaker = new CircuitBreaker(1, 1);
        assertEquals(ADMITTED, breaker.acquire(BAN, T));
        breaker.release(BAN, T);
        assertEquals(ADMITTED, breaker.acquire(BAN, T + 1));
    }

    @Test
    void zeroMeansTheFirstAttemptTrips() {
        CircuitBreaker breaker = new CircuitBreaker(0, 0);
        assertEquals(TRIPPED, breaker.acquire(KICK, T));
    }

    @Test
    void restoreSeedsWindowsAndLatchedState() {
        CircuitBreaker breaker = new CircuitBreaker(3, 5);
        breaker.restore(List.of(T, T + 1, T + 2), List.of(), false, 0, "");
        assertEquals(TRIPPED, breaker.acquire(BAN, T + 3));
        CircuitBreaker open = new CircuitBreaker(3, 5);
        open.restore(List.of(), List.of(), true, T, "earlier");
        assertEquals(OPEN, open.acquire(KICK, T + 1));
        assertEquals("earlier", open.status(T).reason());
    }
}
