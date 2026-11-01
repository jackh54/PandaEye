package dev.pandascript.pandaeye.paper.telemetry;

import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.EventSink;
import dev.pandascript.pandaeye.common.transport.SinkDrain;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.PlayerChecks;
import dev.pandascript.pandaeye.paper.config.ConfigLoader;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.EntityIndex;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionSignals;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.MoveSample;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.bukkit.configuration.file.YamlConfiguration;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.function.Predicate;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Out of focus the plugin sends 2 MoveSamples per second. The first attack, placement or dig after a pause
 * then reaches the cloud without the positions just before it, and the cloud has no eye path (CHECKS 2.1):
 * a moving player's legit click looks off by up to a tick of movement. SessionTelemetry sends the moves it
 * held back right before the action.
 */
class SessionTelemetryCatchUpTest {

    private static final long MS = 1_000_000L;
    private static final long TICK = 50 * MS;
    private static final long T0 = 1_000_000_000_000L;
    private static final ClientInfo CLIENT = new ClientInfo(774, "1.21.11", true, true, true);
    private static final double STEP = 0.2; // blocks per tick: walking

    private final EventSink sink = new EventSink(100_000);
    private final PluginConfig config = new ConfigLoader().load(new YamlConfiguration());
    private final Telemetry telemetry = new Telemetry(sink, new EventClock(0, 0, () -> 0), () -> config, new EntityIndex());
    private final Session session = new Session(null, UUID.randomUUID(), "p", false, CLIENT,
            new SessionSignals(false, () -> config, (from, to) -> false), T0);
    private final PacketProcessor processor = new PacketProcessor(session.state(),
            new PlayerChecks(session.state(), List.of(), (site, error) -> {
                throw new AssertionError(site, error);
            }), telemetry.forSession(session), nanos -> nanos / 1_000);
    private long now = T0;
    private double x = 0.5;
    private int flyingPackets;

    private void join() {
        processor.joinGame(1, GameMode.GAME_MODE_SURVIVAL, "minecraft:overworld", null, now);
        processor.playerLoaded();
        processor.tickEnd(now);
        now += TICK;
    }

    /** {@code ticks} client ticks walking east, one position packet each. */
    private void walk(int ticks) {
        for (int i = 0; i < ticks; i++) {
            x += STEP;
            processor.flying(x, 64, 0.5, 0, 60, true, false, true, false, false, now);
            flyingPackets++;
            processor.tickEnd(now);
            now += TICK;
        }
    }

    /** The action in the current tick, then that tick's movement packet (the vanilla order). */
    private void act(String action) {
        switch (action) {
            case "place" -> processor.place(2, 63, 0, 1, 0.5f, 1f, 0.5f, 0, false, now);
            case "dig" -> processor.dig(CheckInput.DigAction.START, 2, 63, 0, 1, now);
            case "attack" -> processor.interact(77, true, now);
            default -> throw new IllegalArgumentException(action);
        }
        walk(1);
    }

    private static boolean isAction(Event e) {
        return e.hasPlace() || e.hasDig() || e.hasAttack();
    }

    private static List<Event> movesBefore(List<Event> events, Predicate<Event> at) {
        List<Event> out = new ArrayList<>();
        for (Event e : events) {
            if (at.test(e)) {
                return out;
            }
            if (e.hasMove()) {
                out.add(e);
            }
        }
        throw new AssertionError("no such event");
    }

    /** Event times never go back (the cloud drops a player's event older than one it has applied). */
    private static void assertOrdered(List<Event> events) {
        for (int i = 1; i < events.size(); i++) {
            assertTrue(events.get(i).getTimeUs() >= events.get(i - 1).getTimeUs(), "event " + i + " goes back in time");
        }
    }

    /**
     * Every flying packet is counted exactly once by packets_since_last (the tests end in focus, so nothing
     * is held back at the end), and ticks_since_last matches the ticks between the samples.
     */
    private void assertPacketsAccounted(List<Event> events) {
        int packets = 0;
        Event prev = null;
        for (Event e : events) {
            if (!e.hasMove()) {
                continue;
            }
            packets += e.getMove().getPacketsSinceLast();
            if (prev != null) {
                assertEquals(e.getTick() - prev.getTick(), e.getMove().getTicksSinceLast(), "ticks_since_last at " + e.getTick());
            }
            prev = e;
        }
        assertEquals(flyingPackets, packets, "packets_since_last sum");
    }

    @ParameterizedTest
    @ValueSource(strings = {"place", "dig", "attack"})
    void theFirstActionAfterAPauseSendsTheMovesJustBeforeIt(String action) {
        join();
        walk(100); // 5 s without an action: out of focus, 2 Hz
        List<Event> before = SinkDrain.drain(sink);
        long sampled = before.stream().filter(Event::hasMove).count();
        assertTrue(sampled <= 12, "out of focus the moves are sampled: " + sampled + " of 100 sent");

        act(action);
        walk(5);
        List<Event> events = new ArrayList<>(before);
        events.addAll(SinkDrain.drain(sink));
        assertOrdered(events);
        assertPacketsAccounted(events);

        Event act = events.stream().filter(SessionTelemetryCatchUpTest::isAction).findFirst().orElseThrow();
        List<Event> moves = movesBefore(events, SessionTelemetryCatchUpTest::isAction);
        Event last = moves.get(moves.size() - 1);
        Event previous = moves.get(moves.size() - 2);
        assertEquals(act.getTick() - 1, last.getTick(), "the movement packet of the tick before the action");
        assertEquals(act.getTick() - 2, previous.getTick(), "and the one before it (the eye path's start)");
        MoveSample m = last.getMove();
        assertTrue(m.getHasPos());
        assertEquals(x - 6 * STEP, m.getX(), 1e-9, "the position before the action's tick");
        assertEquals(1, m.getTicksSinceLast());
        assertEquals(1, m.getPacketsSinceLast());
        assertFalse(m.getFocus(), "held-back moves were outside focus when they arrived");
        assertEquals(TICK / 1_000, act.getTimeUs() - last.getTimeUs(), "sent with their own times");

        // From the action on, every packet is sent (focus), each once.
        List<Event> after = events.subList(events.indexOf(act) + 1, events.size()).stream().filter(Event::hasMove).toList();
        assertEquals(6, after.size());
        assertEquals(act.getTick(), after.get(0).getTick(), "the action's own tick (R1)");
        assertTrue(after.stream().allMatch(e -> e.getMove().getFocus() && e.getMove().getPacketsSinceLast() == 1));
    }

    @Test
    void heldMovesOlderThanAnOfferedEventAreLeftOut() {
        join();
        walk(97);
        processor.clientKeepAlive(42, now); // an unmatched reply: a LatencySample is offered now
        walk(2);
        act("place");
        List<Event> events = SinkDrain.drain(sink);
        assertOrdered(events);
        assertPacketsAccounted(events);

        Event act = events.stream().filter(Event::hasPlace).findFirst().orElseThrow();
        Event latency = events.stream().filter(Event::hasLatency).findFirst().orElseThrow();
        List<Event> between = events.subList(events.indexOf(latency) + 1, events.indexOf(act));
        assertEquals(List.of(act.getTick() - 2, act.getTick() - 1), between.stream().map(Event::getTick).toList());
        // the moves before the latency event that were held back are counted by the first one sent after it
        assertTrue(between.get(0).getMove().getPacketsSinceLast() > 1);
    }

    @Test
    void inFocusNothingIsHeldOrSentTwice() {
        join();
        walk(100);
        act("place");
        walk(20); // in focus: every packet is sent
        act("place");
        walk(3);
        List<Event> events = SinkDrain.drain(sink);
        assertOrdered(events);
        assertPacketsAccounted(events);
        List<Event> places = events.stream().filter(Event::hasPlace).toList();
        List<Event> between = events.subList(events.indexOf(places.get(0)) + 1, events.indexOf(places.get(1)));
        assertEquals(21, between.stream().filter(Event::hasMove).count(), "one MoveSample per packet in focus");
        assertTrue(between.stream().filter(Event::hasMove).allMatch(e -> e.getMove().getFocus()));
        long distinct = events.stream().filter(Event::hasMove).map(Event::getTimeUs).distinct().count();
        assertEquals(events.stream().filter(Event::hasMove).count(), distinct, "no packet is sent twice");
    }
}
