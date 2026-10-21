package dev.pandascript.pandaeye.paper.stall;

import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.EventSink;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionSignals;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class StallDetectorTest {

    private static final long MS = 1_000_000L;
    private static final long T0 = 1_000_000_000_000L;
    private static final ClientInfo CLIENT = new ClientInfo(774, "1.21.11", true, true, true);

    private final List<Session> sessions = new ArrayList<>();
    private final EventSink sink = new EventSink(100);
    private final StallDetector detector = new StallDetector(() -> sessions,
            new Telemetry(sink, new EventClock(0, 0, System::nanoTime), () -> null, null));

    private List<Session> players(int count) {
        List<Session> added = new ArrayList<>();
        for (int i = 0; i < count; i++) {
            Session session = new Session(null, UUID.randomUUID(), "p" + sessions.size(), false, CLIENT,
                    new SessionSignals(false, () -> null, (from, to) -> false), T0);
            sessions.add(session);
            added.add(session);
        }
        return added;
    }

    private static void packets(List<Session> group, long nanos) {
        group.forEach(session -> session.beforePacket(nanos, true));
    }

    @Test
    void activePlayersAreNoStall() {
        List<Session> all = players(10);
        for (long t = T0; t < T0 + 2_000 * MS; t += 50 * MS) {
            packets(all, t);
            detector.check(t);
        }
        assertFalse(detector.inStall());
        assertEquals(0, detector.stalls());
        assertEquals(0, sink.size());
        assertFalse(detector.overlaps(T0, T0 + 2_000 * MS));
    }

    @Test
    void aFifthOfThePlayersSilentFor400MsIsAStallThatIsReportedWhenItEnds() {
        List<Session> silent = players(3);
        List<Session> talking = players(7);
        packets(silent, T0);
        for (long t = T0; t <= T0 + 350 * MS; t += 50 * MS) {
            packets(talking, t);
            detector.check(t);
        }
        assertFalse(detector.inStall(), "350 ms of silence is not a stall yet");
        long during = T0 + 450 * MS;
        packets(talking, during);
        detector.check(during);
        assertTrue(detector.inStall());
        assertTrue(detector.overlaps(during, during + 10 * MS), "an ongoing stall overlaps the present");
        assertEquals(0, sink.size());

        long end = T0 + 700 * MS;
        packets(sessions, end);
        detector.check(end);
        assertFalse(detector.inStall());
        assertEquals(1, detector.stalls());
        assertEquals(1, sink.size(), "one ServerStall event");
        assertTrue(detector.overlaps(T0 + 100 * MS, T0 + 200 * MS), "the stall started 400 ms before detection");
        assertTrue(detector.overlaps(end - MS, end + MS));
        assertFalse(detector.overlaps(end + 10 * MS, end + 500 * MS));
        assertFalse(detector.overlaps(T0 - 500 * MS, T0 - 10 * MS));
    }

    @Test
    void fewerThanThreeOrUnderAFifthSilentIsNoStall() {
        List<Session> silentTwo = players(2);
        List<Session> talkingThree = players(3);
        packets(silentTwo, T0);
        packets(talkingThree, T0 + 600 * MS);
        detector.check(T0 + 600 * MS);
        assertFalse(detector.inStall(), "40% silent but only 2 players");

        sessions.clear();
        List<Session> silentThree = players(3);
        List<Session> talkingThirteen = players(13);
        packets(silentThree, T0);
        packets(talkingThirteen, T0 + 600 * MS);
        detector.check(T0 + 600 * MS);
        assertFalse(detector.inStall(), "3 of 16 is under 20%");
    }

    @Test
    void idlePlayersDoNotCountAsActive() {
        List<Session> afk = players(20);
        List<Session> silent = players(3);
        List<Session> talking = players(7);
        packets(afk, T0 - 11_000 * MS);
        packets(silent, T0);
        packets(talking, T0 + 500 * MS);
        detector.check(T0 + 500 * MS);
        assertTrue(detector.inStall(), "3 of 10 active players silent; 20 players idle for over 10 s are ignored");
    }

    @Test
    void aLateGlobalHeartbeatIsAStall() {
        List<Session> all = players(5);
        long beat = System.nanoTime();
        detector.heartbeat();
        packets(all, beat + 300 * MS);
        detector.check(beat + 300 * MS);
        assertFalse(detector.inStall());
        packets(all, beat + 450 * MS);
        detector.check(beat + 450 * MS);
        assertTrue(detector.inStall(), "heartbeat more than 400 ms late");
    }

    @Test
    void noHeartbeatYetIsNoStall() {
        List<Session> all = players(5);
        packets(all, T0 + 5_000 * MS);
        detector.check(T0 + 5_000 * MS);
        assertFalse(detector.inStall());
    }

    @Test
    void theStallHistoryIsBounded() {
        List<Session> silent = players(3);
        List<Session> talking = players(2);
        long t = T0;
        for (int i = 0; i < 100; i++) {
            packets(silent, t);
            packets(talking, t + 500 * MS);
            detector.check(t + 500 * MS);
            packets(silent, t + 600 * MS);
            detector.check(t + 600 * MS);
            t += 1_000 * MS;
        }
        assertEquals(100, detector.stalls());
        assertEquals(100, sink.size());
        assertFalse(detector.overlaps(T0, T0 + 600 * MS), "the oldest stalls were dropped from the history");
        assertTrue(detector.overlaps(t - 1_000 * MS, t - 500 * MS));
    }
}
