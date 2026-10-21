package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class NoSlowCheckTest {

    private static final double GROUND = 0.6 * 0.91;
    private static final double ICE = 0.98 * 0.91;
    /** Vanilla input while using an item: 0.98 impulse scaled by 0.2. */
    private static final double USE_INPUT = 0.98 * 0.2;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_NOSLOW, NoSlowCheck::new).join().stand(45);
    }

    /** C USE_ITEM, the server's "hand active" metadata, and the client acknowledging it. */
    private static CheckHarness startEating(CheckHarness harness) {
        harness.processor().useItem(0, harness.now());
        harness.processor().metadata(null, 0x01, harness.now());
        harness.acknowledge();
        return harness;
    }

    /** Moves on flat ground for {@code ticks} ticks with acceleration {@code accel} (from speed {@code h}). */
    private static double walk(CheckHarness harness, double h, double accel, double friction, int ticks) {
        for (int i = 0; i < ticks; i++) {
            h = h * friction + accel;
            harness.step(h, 0, 0, true);
        }
        return h;
    }

    @Test
    void eatingWhileWalkingOrSprintingSlowlyIsLegit() {
        CheckHarness harness = startEating(harness());
        double h = walk(harness, 0.2, 0.1 * USE_INPUT, GROUND, 60);
        walk(harness, h, 0.13 * USE_INPUT, GROUND, 60);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void sprintJumpingWhileEatingIsLegit() {
        CheckHarness harness = startEating(harness());
        double h = walk(harness, 0, 0.13 * USE_INPUT, GROUND, 10);
        for (int jump = 0; jump < 10; jump++) {
            double v = Vanilla.JUMP;
            double y = 0;
            h = h * GROUND + 0.13 * USE_INPUT + 0.2;
            boolean previousGround = true;
            while (true) {
                boolean land = y + v <= 0;
                double move = land ? -y : v;
                y += move;
                harness.step(h, move, 0, land);
                if (land) {
                    break;
                }
                v = Vanilla.fall(v, Vanilla.GRAVITY);
                h = h * (previousGround ? GROUND : 0.91) + 0.026 * USE_INPUT;
                previousGround = false;
            }
            h = walk(harness, h * 0.91 + 0.13 * USE_INPUT, 0.13 * USE_INPUT, GROUND, 2);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void sprintingAtFullSpeedWhileEatingIsACandidateThatConfirms() {
        CheckHarness harness = startEating(harness());
        walk(harness, 0, 0.13, GROUND, 12);
        assertTrue(harness.candidates().size() >= 1);
        harness.confirmAll();
        CheckHarness.Flag flag = harness.flags().get(0);
        assertTrue(flag.worldConfirmed());
        assertEquals(CheckCatalog.MOVEMENT_NOSLOW, flag.check());
        double excess = flag.violation().data().get("excess");
        assertEquals(0.13 - 0.13 * 0.2 - 0.01, excess, 1e-6);
        assertEquals(Math.min(1, excess / 0.15), flag.violation().severity(), 1e-9);
    }

    @Test
    void fourFastPacketsAreNotEnough() {
        CheckHarness harness = startEating(harness());
        double h = 0;
        for (int i = 0; i < 10; i++) {
            h = walk(harness, h, 0.13, GROUND, 4);
            h = walk(harness, h * 0.2, 0.13 * USE_INPUT, GROUND, 1);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void nothingIsJudgedBeforeTheClientAppliedTheMetadataOrInTheFirstTicks() {
        CheckHarness pending = harness();
        pending.processor().useItem(0, pending.now());
        pending.processor().metadata(null, 0x01, pending.now());
        walk(pending, 0, 0.13, GROUND, 30);
        assertEquals(0, pending.candidates().size(), "hand active not applied yet");

        CheckHarness noMetadata = harness();
        noMetadata.processor().useItem(0, noMetadata.now());
        walk(noMetadata, 0, 0.13, GROUND, 30);
        assertEquals(0, noMetadata.candidates().size(), "the server never started the use (e.g. full hunger)");
    }

    @Test
    void releaseSlotChangeDropAndServerEndEndTheUse() {
        CheckHarness released = startEating(harness());
        released.processor().dig(CheckInput.DigAction.RELEASE_USE_ITEM, 0, 0, 0, 0, released.now());
        walk(released, 0, 0.13, GROUND, 30);
        assertEquals(0, released.candidates().size());

        CheckHarness slot = startEating(harness());
        slot.processor().clientHeldItem(3, slot.now());
        walk(slot, 0, 0.13, GROUND, 30);
        assertEquals(0, slot.candidates().size());

        CheckHarness dropped = startEating(harness());
        dropped.processor().dig(CheckInput.DigAction.OTHER, 0, 0, 0, 0, dropped.now());
        walk(dropped, 0, 0.13, GROUND, 30);
        assertEquals(0, dropped.candidates().size());

        CheckHarness completed = startEating(harness());
        walk(completed, 0, 0.13 * USE_INPUT, GROUND, 30);
        completed.processor().metadata(null, 0, completed.now());
        walk(completed, 0.06, 0.13, GROUND, 30);
        assertEquals(0, completed.candidates().size(), "the server finished eating; the client follows");

        CheckHarness again = startEating(harness());
        again.processor().clientHeldItem(3, again.now());
        again.tickEnd();
        again.processor().useItem(0, again.now());
        walk(again, 0, 0.13, GROUND, 12);
        assertTrue(again.candidates().size() >= 1, "a new use in a later tick than the slot change is judged");
    }

    @Test
    void itemsThatDoNotSlowAreDiscardedByTheConfirmation() {
        CheckHarness spear = startEating(harness());
        walk(spear, 0, 0.13, GROUND, 12);
        spear.world().usingItemSpeedMultiplier = 1.0;
        spear.confirmAll();
        assertEquals(0, spear.flags().size());
        assertTrue(spear.discards().get(0).contains("slows only"));

        CheckHarness notUsing = startEating(harness());
        walk(notUsing, 0, 0.13, GROUND, 12);
        notUsing.world().usingItemSpeedMultiplier = Double.NaN;
        notUsing.confirmAll();
        assertEquals(0, notUsing.flags().size());
    }

    @Test
    void iceMomentumIsLenientOrDiscardedWithRealFriction() {
        double iceAccel = 0.13 * USE_INPUT * 0.21600002 / Math.pow(0.98, 3);
        CheckHarness sampled = harness();
        sampled.signals().env(EnvFlag.ENV_FLAG_ON_ICE_VALUE, sampled.now());
        sampled.step(0.6, 0, 0, true);
        startEating(sampled);
        walk(sampled, 0.6, iceAccel, ICE, 40);
        assertEquals(0, sampled.candidates().size());

        CheckHarness unsampled = harness();
        unsampled.step(0.6, 0, 0, true);
        startEating(unsampled);
        walk(unsampled, 0.6, iceAccel, ICE, 40);
        assertTrue(unsampled.candidates().size() >= 1, "the netty side assumes stone without an ice sample");
        unsampled.world().maxFriction = 0.98;
        unsampled.world().minFriction = 0.98;
        unsampled.confirmAll();
        assertEquals(0, unsampled.flags().size());
    }

    @Test
    void soulSandAndCrowdsAreSkipped() {
        CheckHarness soul = startEating(harness());
        soul.signals().env(EnvFlag.ENV_FLAG_ON_SOUL_SPEED_BLOCK_VALUE, soul.now());
        walk(soul, 0, 0.13, GROUND, 30);
        assertEquals(0, soul.candidates().size());

        CheckHarness crowd = startEating(harness());
        crowd.signals().env(EnvFlag.ENV_FLAG_NEAR_ENTITY_VALUE, crowd.now());
        walk(crowd, 0, 0.13, GROUND, 30);
        assertEquals(0, crowd.candidates().size());
    }
}
