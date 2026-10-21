package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FlyCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_FLY, FlyCheck::new).join().stand(45);
    }

    /** Leaves the ground with a vanilla jump and then hovers {@code ticks} ticks at a constant dy. */
    private static void jumpThenHover(CheckHarness harness, double dy, int ticks) {
        harness.step(0, 0.42, 0, false).step(0, Vanilla.fall(0.42, Vanilla.GRAVITY), 0, false);
        for (int i = 0; i < ticks; i++) {
            harness.step(0.1, dy, 0, false);
        }
    }

    @Test
    void jumpsAndLongFallsAreLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 20; i++) {
            Vanilla.jump(harness, Vanilla.JUMP, Double.POSITIVE_INFINITY, 0.3);
        }
        Vanilla.fallFor(harness, 60, Vanilla.GRAVITY);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void jumpingOffALedgeAndIntoCeilingsIsLegit() {
        CheckHarness harness = harness();
        for (double headroom : new double[]{0.2, 0.5, 1.0, 1.2}) {
            for (int i = 0; i < 5; i++) {
                Vanilla.jump(harness, Vanilla.JUMP, headroom, 0.35);
                harness.stand(2);
            }
        }
        for (int i = 0; i < 5; i++) {
            Vanilla.fallFor(harness, 8, Vanilla.GRAVITY);
            harness.step(0.1, -0.2, 0, true).stand(3);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void hoveringBecomesACandidateOnTheSecondPacketAndConfirmsInOpenAir() {
        CheckHarness harness = harness();
        jumpThenHover(harness, 0, 3);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        assertEquals(1, harness.flags().size());
        CheckHarness.Flag flag = harness.flags().get(0);
        assertTrue(flag.worldConfirmed());
        assertEquals(CheckCatalog.MOVEMENT_FLY, flag.check());
        assertEquals(0.0784, flag.violation().data().get("diff"), 1e-9);
        assertEquals(0.0784 / 0.5, flag.violation().severity(), 1e-9);
        assertEquals(1, flag.violation().vlIncrement());
    }

    @Test
    void slowFallingWithoutTheEffectIsACandidate() {
        CheckHarness harness = harness();
        jumpThenHover(harness, -0.05, 6);
        assertTrue(harness.candidates().size() >= 2);
    }

    @Test
    void aSingleBoostAboveHalfABlockIsACandidateAtOnce() {
        CheckHarness harness = harness();
        harness.step(0, 0.42, 0, false).step(0, 0.3332, 0, false).step(0, 1.2, 0, false);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        assertEquals(1.0, harness.flags().get(0).violation().severity(), 1e-9);
    }

    @Test
    void oneOddPacketAloneIsNotACandidate() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            harness.step(0, 0.42, 0, false).step(0, 0.3332, 0, false).step(0, 0.3, 0, false);
            harness.step(0, Vanilla.fall(0.3, Vanilla.GRAVITY), 0, false).step(0, -0.7, 0, true).stand(2);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void fallingThroughAnUnloadedChunkAtMinusPoint098IsLegit() {
        CheckHarness harness = harness();
        harness.step(0, 0.42, 0, false).step(0, 0.3332, 0, false);
        for (int i = 0; i < 40; i++) {
            harness.step(0, -0.098, 0, false);
        }
        assertEquals(0, harness.candidates().size());
        harness.step(0, -0.06, 0, false).step(0, -0.06, 0, false);
        assertEquals(1, harness.candidates().size(), "anything else than -0.098 is judged");
    }

    @Test
    void obstaclesAndExemptSurroundingsAreDiscardedByTheConfirmation() {
        CheckHarness blocks = harness();
        jumpThenHover(blocks, 0, 3);
        blocks.world().collisions = box -> true;
        blocks.confirmAll();
        assertEquals(0, blocks.flags().size());
        assertTrue(blocks.discards().get(0).contains("open air"));

        CheckHarness crowd = harness();
        jumpThenHover(crowd, 0, 3);
        crowd.world().exemptNear = box -> true;
        crowd.confirmAll();
        assertEquals(0, crowd.flags().size());
        assertTrue(crowd.discards().get(0).contains("exempt"));
    }

    @Test
    void crowdsLiquidsAndClimbablesSeenByTheSamplerSkipTheCheck() {
        for (int flag : new int[]{EnvFlag.ENV_FLAG_NEAR_ENTITY_VALUE, EnvFlag.ENV_FLAG_IN_WATER_VALUE,
                EnvFlag.ENV_FLAG_CLIMBING_VALUE, EnvFlag.ENV_FLAG_IN_WEB_VALUE}) {
            CheckHarness harness = harness();
            harness.signals().env(flag, harness.now());
            jumpThenHover(harness, 0, 10);
            assertEquals(0, harness.candidates().size(), "env flag " + flag);
        }
    }

    @Test
    void slimeBouncesAreLegit() {
        CheckHarness harness = harness();
        Vanilla.fallFor(harness, 12, Vanilla.GRAVITY);
        double v = -1.0;
        for (int bounce = 0; bounce < 4; bounce++) {
            harness.step(0, -0.3, 0, true);
            v = Vanilla.fall(-v, Vanilla.GRAVITY);
            while (v > -0.9) {
                harness.step(0, v, 0, false);
                v = Vanilla.fall(v, Vanilla.GRAVITY);
            }
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void levitationEndingIsLenientUntilTheClientAppliedIt() {
        CheckHarness harness = harness();
        harness.processor().effect(EffectTracker.LEVITATION, 0, harness.now());
        harness.acknowledge();
        double v = 0;
        for (int i = 0; i < 40; i++) {
            v = (v + (0.05 - v) * 0.2) * Vanilla.DRAG;
            harness.step(0, v, 0, false);
        }
        harness.processor().effectRemoved(EffectTracker.LEVITATION, harness.now());
        for (int i = 0; i < 10; i++) {
            v = (v + (0.05 - v) * 0.2) * Vanilla.DRAG;
            harness.step(0, v, 0, false);
        }
        harness.acknowledge();
        for (int i = 0; i < 2; i++) {
            v = (v + (0.05 - v) * 0.2) * Vanilla.DRAG;
            harness.step(0, v, 0, false);
        }
        for (int i = 0; i < 30; i++) {
            v = Vanilla.fall(v, Vanilla.GRAVITY);
            harness.step(0, v, 0, false);
        }
        assertEquals(0, harness.candidates().size(), "the client may apply the removal a frame late");
    }

    @Test
    void slowFallingEndingIsLenientUntilTheClientAppliedIt() {
        CheckHarness harness = harness();
        harness.processor().effect(EffectTracker.SLOW_FALLING, 0, harness.now());
        harness.acknowledge();
        double v = Vanilla.fall(0.42, Vanilla.GRAVITY);
        harness.step(0, 0.42, 0, false);
        for (int i = 0; i < 40; i++) {
            harness.step(0, v, 0, false);
            v = Vanilla.fall(v, v <= 0 ? 0.01 : Vanilla.GRAVITY);
        }
        harness.processor().effectRemoved(EffectTracker.SLOW_FALLING, harness.now());
        for (int i = 0; i < 6; i++) {
            harness.step(0, v, 0, false);
            v = Vanilla.fall(v, 0.01);
        }
        harness.acknowledge();
        for (int i = 0; i < 30; i++) {
            harness.step(0, v, 0, false);
            v = Vanilla.fall(v, Vanilla.GRAVITY);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void aLowerGravityCountsAsSoonAsTheServerSendsIt() {
        CheckHarness harness = harness();
        harness.processor().attributes(Map.of(AttributeTracker.GRAVITY, 0.02), harness.now());
        harness.step(0, 0.42, 0, false);
        double v = Vanilla.fall(0.42, 0.02);
        for (int i = 0; i < 40; i++) {
            harness.step(0, v, 0, false);
            v = Vanilla.fall(v, 0.02);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void pendingKnockbackAndExplosionsSuspendTheCheck() {
        CheckHarness knockback = harness();
        knockback.processor().velocity(0.4, 0.36, 0, VelocityTracker.Source.PACKET, knockback.now());
        knockback.acknowledge();
        knockback.step(0.4, 0.36, 0, false);
        double v = Vanilla.fall(0.36, Vanilla.GRAVITY);
        for (int i = 0; i < 20; i++) {
            knockback.step(0.3, v, 0, false);
            v = Vanilla.fall(v, Vanilla.GRAVITY);
        }
        assertEquals(0, knockback.candidates().size());

        CheckHarness explosion = harness();
        explosion.processor().velocity(0, 1.1, 0, VelocityTracker.Source.EXPLOSION, explosion.now());
        explosion.step(0, 0.42, 0, false).step(0, 1.5, 0, false).step(0, 1.6, 0, false);
        assertEquals(0, explosion.candidates().size());
    }

    @Test
    void riptideInRainAndElytraStartAreProvisionallyExempt() {
        CheckHarness riptide = harness();
        riptide.processor().dig(CheckInput.DigAction.RELEASE_USE_ITEM, 0, 0, 0, 0, riptide.now());
        riptide.step(1.5, 2.0, 0, false).step(1.4, 1.9, 0, false).step(1.3, 1.8, 0, false);
        assertEquals(0, riptide.candidates().size());

        CheckHarness elytra = harness();
        elytra.step(0, 0.42, 0, false).step(0, 0.3332, 0, false);
        elytra.processor().startElytra(elytra.now());
        for (int i = 0; i < 10; i++) {
            elytra.step(1.0, -0.02, 0, false);
        }
        elytra.processor().metadata(0x80, null, elytra.now());
        for (int i = 0; i < 40; i++) {
            elytra.step(1.0, -0.02, 0, false);
        }
        assertEquals(0, elytra.candidates().size());
    }

    @Test
    void flyingAndAFlightThatJustEndedAreExempt() {
        CheckHarness harness = harness();
        harness.processor().serverAbilities(new Abilities(true, true, false, 0.05f), harness.now());
        harness.acknowledge();
        jumpThenHover(harness, 0, 10);
        harness.processor().serverAbilities(new Abilities(false, false, false, 0.05f), harness.now());
        harness.acknowledge();
        for (int i = 0; i < 10; i++) {
            harness.step(0, 0, 0, false);
        }
        assertEquals(0, harness.candidates().size(), "within a second of flying");
    }

    @Test
    void legacyClientsAndTeleportsAreSkipped() {
        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.MOVEMENT_FLY, FlyCheck::new)
                .join().stand(45);
        jumpThenHover(legacy, 0, 10);
        assertEquals(0, legacy.candidates().size());

        CheckHarness teleported = harness();
        teleported.processor().serverTeleport(3, 0, 80, 0, 0, 0, false, false, false, false, false, 0, 0, 0,
                teleported.now());
        jumpThenHover(teleported, 0, 4);
        assertEquals(0, teleported.candidates().size());
    }
}
