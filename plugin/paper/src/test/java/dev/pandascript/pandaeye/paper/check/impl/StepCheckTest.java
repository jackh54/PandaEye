package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class StepCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_STEP, StepCheck::new).join().stand(45);
    }

    @Test
    void walkingUpStairsAndSlabsIsLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 30; i++) {
            harness.step(0.2, 0.5, 0, true).step(0.2, 0, 0, true);
        }
        for (int i = 0; i < 10; i++) {
            harness.step(0.25, 0.6, 0, true).step(0.25, 0, 0, true);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void jumpingAndSprintJumpingUnderA2BlockCeilingAreLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            Vanilla.jump(harness, Vanilla.JUMP, Double.POSITIVE_INFINITY, 0.3);
        }
        for (int i = 0; i < 10; i++) {
            Vanilla.jump(harness, Vanilla.JUMP, 0.2, 0.6);
            harness.step(0.4, 0, 0, true);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void aHighJumpIsACandidateAtOnceAndConfirmsInOpenAir() {
        CheckHarness harness = harness();
        harness.step(0, 1.25, 0, false);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        CheckHarness.Flag flag = harness.flags().get(0);
        assertTrue(flag.worldConfirmed());
        assertEquals(CheckCatalog.MOVEMENT_STEP, flag.check());
        double excess = 1.25 - 0.601;
        assertEquals(excess, flag.violation().data().get("excess"), 1e-9);
        assertEquals(Math.min(1, excess / 0.5), flag.violation().severity(), 1e-9);
    }

    @Test
    void aStepUpOntoABlockIsDiscardedBecauseTheBlockIsWithinOneBlock() {
        CheckHarness harness = harness();
        harness.step(0.3, 1.0, 0, true);
        assertEquals(1, harness.candidates().size());
        harness.world().collisions = box -> box.minY() < 65;
        harness.confirmAll();
        assertEquals(0, harness.flags().size());
        assertTrue(harness.discards().get(0).contains("open air"));
    }

    @Test
    void jumpBoostCountsWhilePendingAndForASecondAfterItExpires() {
        CheckHarness harness = harness();
        harness.processor().effect(EffectTracker.JUMP_BOOST, 1, harness.now());
        harness.step(0, 0.62, 0, false).step(0, -0.62, 0, true);
        harness.acknowledge();
        harness.stand(2).step(0, 0.62, 0, false).step(0, -0.62, 0, true);
        harness.processor().effectRemoved(EffectTracker.JUMP_BOOST, harness.now());
        harness.stand(2).step(0, 0.62, 0, false).step(0, -0.62, 0, true);
        harness.acknowledge();
        harness.stand(2).step(0, 0.62, 0, false).step(0, -0.62, 0, true);
        assertEquals(0, harness.candidates().size(), "a frame-late expiry must not flag");
        harness.stand(25).step(0, 0.62, 0, false);
        assertEquals(1, harness.candidates().size(), "jump boost gone for over a second");
    }

    @Test
    void higherStepHeightAndJumpStrengthCountAsSoonAsSent() {
        CheckHarness harness = harness();
        harness.processor().attributes(Map.of(AttributeTracker.STEP_HEIGHT, 1.0, AttributeTracker.JUMP_STRENGTH, 0.8),
                harness.now());
        harness.step(0.3, 1.0, 0, true).stand(2).step(0, 0.8, 0, false);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void slimeBouncesAreSkippedOrDiscarded() {
        CheckHarness sampled = harness();
        sampled.signals().env(EnvFlag.ENV_FLAG_ON_SLIME_VALUE, sampled.now());
        sampled.step(0, -0.4, 0, true).step(0, 0.9, 0, false);
        assertEquals(0, sampled.candidates().size());

        CheckHarness unsampled = harness();
        unsampled.step(0, -0.4, 0, true).step(0, 0.9, 0, false);
        unsampled.world().exemptNear = box -> true;
        unsampled.confirmAll();
        assertEquals(0, unsampled.flags().size());
        assertTrue(unsampled.discards().get(0).contains("exempt"));
    }

    @Test
    void knockbackRiptideAndElytraAreExempt() {
        CheckHarness knockback = harness();
        knockback.processor().velocity(0, 0.9, 0, VelocityTracker.Source.EXPLOSION, knockback.now());
        knockback.step(0, 0.9, 0, false);
        assertEquals(0, knockback.candidates().size());

        CheckHarness riptide = harness();
        riptide.processor().dig(CheckInput.DigAction.RELEASE_USE_ITEM, 0, 0, 0, 0, riptide.now());
        riptide.step(0.5, 2.5, 0, false);
        assertEquals(0, riptide.candidates().size());

        CheckHarness elytra = harness();
        elytra.processor().startElytra(elytra.now());
        elytra.step(0.5, 1.5, 0, false);
        assertEquals(0, elytra.candidates().size());
    }

    @Test
    void airborneRisesAndLegacyClientsAreNotJudged() {
        CheckHarness harness = harness();
        harness.step(0, 0.42, 0, false).step(0, 1.5, 0, false);
        assertEquals(0, harness.candidates().size(), "movement.fly judges airborne packets");

        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.MOVEMENT_STEP, StepCheck::new)
                .join().stand(45);
        legacy.step(0, 1.5, 0, false);
        assertEquals(0, legacy.candidates().size());
    }
}
