package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.impl.PitchCheck;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import dev.pandascript.pandaeye.protocol.v1.MoveFlag;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PacketStateTest {

    private static final long MS = 1_000_000L;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_PITCH, PitchCheck::new).join();
    }

    @Test
    void aServerChangeIsAppliedOneMovementAfterItsAcknowledgement() {
        CheckHarness h = harness().stand(1);
        PacketProcessor p = h.processor();
        PacketState state = h.state();
        p.attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.05), h.now());
        h.stand(3);
        assertEquals(0.1, state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED), 1e-9, "lenient while pending");
        h.acknowledge();
        h.stand(1);
        assertEquals(0.05, state.attributes().get(AttributeTracker.MOVEMENT_SPEED).applied(), 1e-9,
                "applied after the move that followed the acknowledgement");
        assertFalse(state.attributes().get(AttributeTracker.MOVEMENT_SPEED).isPending());
    }

    @Test
    void withoutAnAcknowledgementChangesApplyAfterFiveSeconds() {
        CheckHarness h = harness().stand(1);
        h.processor().gameMode(GameMode.GAME_MODE_CREATIVE, h.now());
        h.stand(10);
        assertTrue(h.state().gameMode().isPending());
        h.advance(5_000 * MS).stand(1);
        assertEquals(GameMode.GAME_MODE_CREATIVE, h.state().gameMode().applied());
    }

    @Test
    void keepAliveIdsSentTwiceAreMarkedInjectedAndRepliesMatchInOrder() {
        AckTracker acks = new AckTracker();
        acks.serverKeepAlive(1, 100 * MS);
        acks.serverPing(9, 110 * MS);
        acks.serverKeepAlive(1, 120 * MS);
        acks.serverKeepAlive(2, 130 * MS);
        AckTracker.Reply first = acks.clientKeepAlive(1, 150 * MS);
        assertTrue(first.matched());
        assertTrue(first.injected());
        assertEquals(50 * MS, first.rttNanos());
        assertEquals(100 * MS, acks.ackedUpTo(), "the older send of a duplicated id is the conservative boundary");
        assertTrue(acks.clientPong(9, 160 * MS).matched());
        assertEquals(110 * MS, acks.ackedUpTo());
        assertFalse(acks.clientKeepAlive(77, 170 * MS).matched());
        assertTrue(acks.clientKeepAlive(2, 180 * MS).matched());
        assertEquals(130 * MS, acks.ackedUpTo());
        acks.serverKeepAlive(5, 200 * MS);
        acks.serverKeepAlive(6, 40_000 * MS);
        assertFalse(acks.clientKeepAlive(5, 40_001 * MS).matched(), "entries expire after 30 s");
    }

    @Test
    void rttUsesThePongMedianThenKeepAliveThenTheDefault() {
        RttTracker rtt = new RttTracker();
        assertEquals(100, rtt.pingMillis(0));
        rtt.keepAlive(80 * MS);
        assertEquals(80, rtt.pingMillis(0));
        long now = 1_000 * MS;
        for (long sample : new long[]{30, 50, 40, 200, 45}) {
            rtt.pong(sample * MS, now);
        }
        assertEquals(45, rtt.pingMillis(now));
        assertEquals(200, rtt.rttHighMillis(now));
        assertEquals(80, rtt.pingMillis(now + 31_000 * MS), "pongs older than 30 s no longer count");
    }

    @Test
    void teleportsArePendingUntilConfirmedAndThreePositionsLater() {
        CheckHarness h = harness().stand(1);
        PacketState state = h.state();
        h.processor().serverTeleport(3, 5, 1, 0, 0, 0, true, true, true, false, false, 0, 0, 0, h.now());
        assertEquals(1, state.teleports().unconfirmed());
        assertTrue(state.teleports().pending());
        h.processor().teleportConfirm(3, h.now());
        h.processor().flying(state.x() + 5, state.y() + 1, state.z(), 0, 0, true, true, false, false, false, h.now());
        h.tickEnd();
        Move teleport = state.moves().get(0);
        assertTrue(teleport.teleport());
        assertTrue(teleport.has(MoveFlag.MOVE_FLAG_SERVER_ROTATION_VALUE));
        assertEquals(0, teleport.dx(), 1e-9, "the teleport PosRot is a new baseline, not a movement");
        h.stand(1);
        assertTrue(state.teleports().pending(), "the third position packet after the teleport is still skipped");
        h.stand(1);
        assertFalse(state.teleports().pending(), "three position packets have followed: the next one is checked");
    }

    @Test
    void slotMirrorExpectsOneEchoPerServerChange() {
        SlotTracker slots = new SlotTracker();
        slots.reset(0);
        assertEquals(SlotTracker.Change.CHANGED, slots.clientSet(4));
        slots.serverSet(7);
        assertEquals(SlotTracker.Change.ECHO, slots.clientSet(7));
        assertEquals(SlotTracker.Change.REDUNDANT, slots.clientSet(7));
        assertEquals(SlotTracker.Change.OUT_OF_RANGE, slots.clientSet(9));
        slots.serverSet(7);
        assertEquals(0, slots.expectedEchoes(), "setting the slot the client already has expects no echo");
    }

    @Test
    void containersTrackReplacedAndClosedIdsWithAcknowledgement() {
        ContainerTracker containers = new ContainerTracker();
        assertFalse(containers.known());
        containers.open(1, "minecraft:generic_9x3", 10);
        containers.open(2, "minecraft:furnace", 20);
        assertEquals(2, containers.openId());
        assertTrue(containers.closedAndApplied(1, sent -> sent <= 25), "replaced and acknowledged");
        assertFalse(containers.closedAndApplied(1, sent -> false), "replaced but not applied yet");
        containers.clientClose(2, 30);
        assertTrue(containers.closedAndApplied(2, sent -> false), "a client-side close is applied by definition");
        assertTrue(containers.everOpened(1));
        assertFalse(containers.everOpened(5));
        containers.unknown();
        assertFalse(containers.known());
    }

    @Test
    void positionHistoryKeepsTheLast64EntriesOldestFirst() {
        PositionHistory history = new PositionHistory();
        for (int i = 0; i < 100; i++) {
            history.add(i * 50_000L, i, 64, 0);
        }
        List<PositionHistory.Entry> recent = history.since(90 * 50_000L, 64);
        assertEquals(10, recent.size());
        assertEquals(90, recent.get(0).x(), 1e-9);
        assertEquals(99, history.latest().x(), 1e-9);
        assertEquals(64, history.since(0, 100).size());
    }

    @Test
    void velocitiesStayPendingUntilMatchedOrAppliedPlusTwoMoves() {
        CheckHarness h = harness().stand(1);
        PacketState state = h.state();
        h.processor().velocity(0, 0.42, 0, VelocityTracker.Source.PACKET, h.now());
        h.stand(1);
        assertTrue(state.velocities().pending());
        h.step(0, 0.42, 0, false);
        assertFalse(state.velocities().pending(), "consumed by the matching movement");

        h.processor().velocity(0.5, 0, 0, VelocityTracker.Source.EXPLOSION, h.now());
        h.acknowledge();
        h.stand(3);
        assertFalse(state.velocities().pending(), "applied plus two movements");
    }

    @Test
    void commonExemptionsFollowTheTrackedState() {
        CheckHarness h = harness().stand(45);
        PacketState state = h.state();
        long now = h.now();
        assertNull(state.movementExemption(now));
        h.signals().env(EnvFlag.ENV_FLAG_IN_WATER_VALUE, now);
        assertEquals(Exemption.ENVIRONMENT, state.movementExemption(now));
        h.signals().env(0, now);
        assertEquals(Exemption.ENVIRONMENT, state.movementExemption(now + 900 * MS), "water seen within 1 s");
        assertNull(state.movementExemption(now + 1_100 * MS));
        h.processor().effect("minecraft:levitation", 0, now);
        assertEquals(Exemption.LEVITATION, state.movementExemption(now + 1_100 * MS));

        CheckHarness flying = harness().stand(45);
        flying.processor().serverAbilities(new Abilities(true, true, false, 0.05f), flying.now());
        assertEquals(Exemption.FLYING, flying.state().movementExemption(flying.now()));

        CheckHarness fresh = harness().stand(5);
        assertEquals(Exemption.WARMING_UP, fresh.state().movementExemption(fresh.now()));

        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.PROTOCOL_PITCH, PitchCheck::new)
                .join().stand(45);
        assertEquals(Exemption.NOT_TICK_CLIENT, legacy.state().movementExemption(legacy.now()));
    }

    @Test
    void consecutiveRequiresExactlyOneTickEndBetweenMovementPackets() {
        CheckHarness h = harness().stand(3);
        assertTrue(h.state().moves().get(0).consecutive());
        h.processor().flying(0, 64, 0, 0, 0, true, false, true, false, false, h.now());
        h.processor().flying(0, 64, 0, 0, 0, true, false, true, false, false, h.now());
        assertFalse(h.state().moves().get(0).consecutive(), "a second movement packet in the same tick");
        h.tickEnd().tickEnd();
        h.stand(1);
        assertFalse(h.state().moves().get(0).consecutive(), "two tick ends in between");
    }

    @Test
    void attributeValuesFollowVanillaOperationOrder() {
        double value = AttributeMath.value(0.1, List.of(
                new AttributeMath.Modifier(AttributeMath.Operation.ADD_MULTIPLIED_TOTAL, 0.2),
                new AttributeMath.Modifier(AttributeMath.Operation.ADD_VALUE, 0.05),
                new AttributeMath.Modifier(AttributeMath.Operation.ADD_MULTIPLIED_BASE, 1.0)));
        assertEquals((0.15 + 0.15) * 1.2, value, 1e-12);
        assertEquals("movement_speed", AttributeTracker.normalise("minecraft:generic.movement_speed"));
        assertEquals("step_height", AttributeTracker.normalise("minecraft:step_height"));
        assertNull(AttributeTracker.normalise("minecraft:armor"));
    }
}
