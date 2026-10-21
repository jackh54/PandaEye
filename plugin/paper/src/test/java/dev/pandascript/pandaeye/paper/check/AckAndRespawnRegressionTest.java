package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.check.impl.SpeedCheck;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Live-safety regressions: acknowledgement by keep-alive and stale state after a death respawn. */
class AckAndRespawnRegressionTest {

    private static final double GROUND = 0.6 * 0.91;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new).join().stand(45);
    }

    /** Steady vanilla ground sprinting at movement speed {@code speed}. */
    private static void sprint(CheckHarness harness, double speed, int ticks) {
        double h = speed * 1.3 * 0.21600002 / Math.pow(GROUND / 0.91, 3) / (1 - GROUND);
        for (int i = 0; i < ticks; i++) {
            harness.step(h, 0, 0, true);
        }
    }

    @Test
    void aKeepAliveAnsweredBeforeTheAttributeIsAppliedDoesNotMakeTheCheckStrict() {
        CheckHarness h = harness();
        h.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.14), h.now());
        h.acknowledge();
        sprint(h, 0.14, 40);
        assertEquals(0, h.candidates().size());

        // Speed II ends: the server sends 0.1 and a keep-alive, which the client answers on its netty thread
        // while its main thread still moves with the old attribute.
        h.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.1), h.now());
        h.keepAlive();
        sprint(h, 0.14, 8);
        assertEquals(0, h.candidates().size(), "keep-alive replies do not acknowledge the attribute");

        h.acknowledge();
        sprint(h, 0.1, 20);
        assertEquals(0, h.candidates().size());
        sprint(h, 0.14, 8);
        assertTrue(h.candidates().size() >= 1, "strict once a PONG acknowledged the change");
    }

    @Test
    void aDeathRespawnDropsAttributeModifiersEntityDataAndEffects() {
        CheckHarness h = harness();
        PacketState state = h.state();
        h.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.05), h.now()); // powder snow freeze
        h.processor().effect("minecraft:slowness", 1, h.now());
        h.processor().metadata(0x02, 0x01, h.now());
        h.acknowledge().stand(2);
        assertEquals(0.05, state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED), 1e-9);

        h.processor().death();
        h.processor().respawn(GameMode.GAME_MODE_SURVIVAL, "minecraft:overworld", 0, h.now());
        h.processor().playerLoaded();
        assertEquals(0.1, state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED), 1e-9,
                "the default counts as soon as the respawn is sent");
        assertEquals(-1, state.effects().minAmplifier(EffectTracker.SLOWNESS), "effects pending-absent");
        h.stand(45);
        sprint(h, 0.1, 40);
        assertEquals(0, h.candidates().size(), "no stale freeze slowdown after the death respawn");

        h.acknowledge().stand(1);
        assertFalse(state.attributes().get(AttributeTracker.MOVEMENT_SPEED).isPending());
        assertEquals(0.1, state.attributes().get(AttributeTracker.MOVEMENT_SPEED).applied(), 1e-9);
        assertFalse(state.effects().present(EffectTracker.SLOWNESS));
        assertFalse(state.handActiveApplied());
    }

    @Test
    void aRespawnThatKeepsAllDataKeepsAttributesAndEntityData() {
        CheckHarness h = harness();
        PacketState state = h.state();
        h.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.2), h.now());
        h.processor().metadata(0x02, null, h.now());
        h.acknowledge().stand(2);
        h.processor().respawn(GameMode.GAME_MODE_SURVIVAL, "minecraft:the_nether",
                PacketState.KEEP_ATTRIBUTES | PacketState.KEEP_ENTITY_DATA, h.now());
        assertFalse(state.attributes().get(AttributeTracker.MOVEMENT_SPEED).isPending());
        assertEquals(0.2, state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED), 1e-9);
    }
}
