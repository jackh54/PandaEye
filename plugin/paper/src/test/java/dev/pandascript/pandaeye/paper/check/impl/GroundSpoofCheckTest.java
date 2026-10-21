package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class GroundSpoofCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_GROUNDSPOOF, GroundSpoofCheck::new).join().stand(45);
    }

    /** Falls for {@code ticks} ticks with vanilla motion while claiming {@code onGround}. */
    private static void fall(CheckHarness harness, int ticks, boolean onGround) {
        double v = Vanilla.fall(0, Vanilla.GRAVITY);
        for (int i = 0; i < ticks; i++) {
            harness.step(0.1, v, 0, onGround);
            v = Vanilla.fall(v, Vanilla.GRAVITY);
        }
    }

    @Test
    void noFallBecomesOneCandidatePerFallAndConfirmsWithoutSupport() {
        CheckHarness harness = harness();
        fall(harness, 30, true);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        CheckHarness.Flag flag = harness.flags().get(0);
        assertTrue(flag.worldConfirmed());
        assertEquals(CheckCatalog.MOVEMENT_GROUNDSPOOF, flag.check());
        double dy = flag.violation().data().get("dy");
        assertTrue(dy < -0.1);
        assertEquals(Math.min(1, -dy), flag.violation().severity(), 1e-9);

        harness.stand(5);
        fall(harness, 10, true);
        assertEquals(1, harness.candidates().size(), "a new fall is a new streak");
    }

    @Test
    void landingsJumpsAndStairsAreLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            fall(harness, 12, false);
            harness.step(0.1, -0.4, 0, true).step(0.1, 0, 0, true);
            Vanilla.jump(harness, Vanilla.JUMP, Double.POSITIVE_INFINITY, 0.3);
            Vanilla.jump(harness, Vanilla.JUMP, 0.2, 0.5);
        }
        for (int i = 0; i < 20; i++) {
            harness.step(0.25, -0.0784, 0, false).step(0.25, -0.155, 0, false).step(0.25, -0.266, 0, true)
                    .step(0.25, 0, 0, true);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void aSingleFallingGroundPacketIsNotEnough() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            fall(harness, 6, false);
            harness.step(0.1, -0.35, 0, true).step(0.1, -0.05, 0, true).stand(2);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void supportingBlocksAndEntitiesAreDiscardedByTheConfirmation() {
        CheckHarness block = harness();
        fall(block, 8, true);
        block.world().collisions = box -> true;
        block.confirmAll();
        assertEquals(0, block.flags().size());
        assertTrue(block.discards().get(0).contains("supporting block"));

        CheckHarness entity = harness();
        fall(entity, 8, true);
        entity.world().entityNear = true;
        entity.confirmAll();
        assertEquals(0, entity.flags().size());
        assertTrue(entity.discards().get(0).contains("entity"));
    }

    @Test
    void velocityEntitiesAndClimbingSkipTheCheck() {
        CheckHarness velocity = harness();
        velocity.processor().velocity(0, -0.5, 0, VelocityTracker.Source.EXPLOSION, velocity.now());
        fall(velocity, 8, true);
        assertEquals(0, velocity.candidates().size());

        for (int flag : new int[]{EnvFlag.ENV_FLAG_NEAR_ENTITY_VALUE, EnvFlag.ENV_FLAG_CLIMBING_VALUE,
                EnvFlag.ENV_FLAG_IN_POWDER_SNOW_VALUE}) {
            CheckHarness env = harness();
            env.signals().env(flag, env.now());
            fall(env, 8, true);
            assertEquals(0, env.candidates().size(), "env flag " + flag);
        }
    }
}
