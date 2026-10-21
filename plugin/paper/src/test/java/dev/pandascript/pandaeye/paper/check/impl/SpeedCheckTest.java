package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SpeedCheckTest {

    private static final double GROUND = 0.6 * 0.91;
    private static final double ICE = 0.98 * 0.91;

    /** A joined, warmed-up player standing on the ground. */
    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new).join().stand(45);
    }

    /** Moves along +x with displacement {@code h} per tick for {@code ticks} ticks. */
    private static void run(CheckHarness harness, double h, int ticks, boolean onGround) {
        for (int i = 0; i < ticks; i++) {
            harness.step(h, 0, 0, onGround);
        }
    }

    /** Vanilla ground sprinting from rest at movement speed {@code speed}: h converges to a / (1 - f). */
    private static void sprint(CheckHarness harness, double speed, double friction, int ticks) {
        double h = 0;
        double accel = speed * 1.3 * 0.21600002 / Math.pow(friction / 0.91, 3);
        for (int i = 0; i < ticks; i++) {
            h = h * friction + accel;
            harness.step(h, 0, 0, true);
        }
    }

    /** Vanilla sprint-jumping on flat ground with friction {@code friction}, {@code jumps} times. */
    private static void sprintJump(CheckHarness harness, double friction, int jumps) {
        double accel = 0.13 * 0.21600002 / Math.pow(friction / 0.91, 3);
        double h = 0;
        for (int i = 0; i < 3; i++) {
            h = h * friction + accel;
            harness.step(h, 0, 0, true);
        }
        for (int j = 0; j < jumps; j++) {
            double dy = 0.42;
            double y = 0;
            boolean previousGround = true;
            h = h * friction + accel + 0.2;
            y += dy;
            harness.step(h, dy, 0, false);
            while (true) {
                dy = (dy - 0.08) * 0.98;
                boolean land = y + dy <= 0;
                h = h * (previousGround ? friction : 0.91) + 0.026;
                previousGround = false;
                double move = land ? -y : dy;
                y += move;
                harness.step(h, move, 0, land);
                if (land) {
                    break;
                }
            }
            h = h * 0.91 + accel;
            harness.step(h, 0, 0, true);
        }
    }

    @Test
    void groundSprintingFromRestIsLegit() {
        CheckHarness harness = harness();
        sprint(harness, 0.1, GROUND, 200);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void sprintJumpingIsLegit() {
        CheckHarness harness = harness();
        sprintJump(harness, GROUND, 15);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void sustainedExcessBecomesACandidateThatTheConfirmationKeeps() {
        CheckHarness harness = harness();
        run(harness, 0.6, 10, true);
        assertTrue(harness.candidates().size() >= 1);
        harness.confirmAll();
        assertTrue(harness.flags().size() >= 1);
        CheckHarness.Flag flag = harness.flags().get(0);
        assertTrue(flag.worldConfirmed());
        assertEquals(CheckCatalog.MOVEMENT_SPEED, flag.check());
        double excess = flag.violation().data().get("excess");
        assertEquals(0.6 - (0.6 * GROUND + 0.13 + 0.01), excess, 1e-6);
        assertEquals(excess / 0.3, flag.violation().severity(), 1e-9);
        assertEquals(1, flag.violation().vlIncrement());
        assertEquals(40, flag.context().size());
    }

    @Test
    void oneHugeExcessIsACandidateAtOnceAndCountsDouble() {
        CheckHarness harness = harness();
        run(harness, 0.2, 3, true);
        harness.step(1.5, 0, 0, true);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        assertEquals(1.0, harness.flags().get(0).violation().severity(), 1e-9);
        assertEquals(2, harness.flags().get(0).violation().vlIncrement());
    }

    @Test
    void smallExcessesDecayInTheBuffer() {
        CheckHarness harness = harness();
        for (int i = 0; i < 30; i++) {
            sprint(harness, 0.1, GROUND, 25);
            harness.step(0.3, 0, 0, true);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void iceMomentumIsDiscardedByTheConfirmationWithRealFriction() {
        CheckHarness harness = harness();
        sprintJump(harness, ICE, 6);
        assertTrue(harness.candidates().size() >= 1, "netty side assumes stone without an ice sample");
        harness.world().maxFriction = 0.98;
        harness.confirmAll();
        assertEquals(0, harness.flags().size());
        assertTrue(harness.discards().get(0).contains("real friction"), harness.discards().toString());
    }

    @Test
    void iceSeenByTheSamplerIsLenientOnTheNettySide() {
        CheckHarness harness = harness();
        harness.signals().env(EnvFlag.ENV_FLAG_ON_ICE_VALUE, harness.now());
        sprintJump(harness, ICE, 3);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void soulSpeedBlocksAndExemptSurroundingsAreDiscarded() {
        CheckHarness soul = harness();
        run(soul, 0.6, 10, true);
        soul.world().soulSpeed = true;
        soul.confirmAll();
        assertEquals(0, soul.flags().size());

        CheckHarness near = harness();
        run(near, 0.6, 10, true);
        near.world().exemptNear = box -> true;
        near.confirmAll();
        assertEquals(0, near.flags().size());
        assertTrue(near.discards().get(0).contains("exempt"));
    }

    @Test
    void aPendingVelocitySuspendsTheCheck() {
        CheckHarness harness = harness();
        harness.processor().velocity(1.2, 0.4, 0, VelocityTracker.Source.PACKET, harness.now());
        run(harness, 1.0, 2, true);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void aFasterSpeedAttributeCountsAsSoonAsTheServerSendsIt() {
        CheckHarness harness = harness();
        harness.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.2), harness.now());
        sprint(harness, 0.2, GROUND, 60);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void aSlowerSpeedAttributeOnlyCountsOnceTheClientAppliedIt() {
        CheckHarness harness = harness();
        harness.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.2), harness.now());
        harness.acknowledge();
        sprint(harness, 0.2, GROUND, 30);
        harness.processor().attributes(Map.of(AttributeTracker.MOVEMENT_SPEED, 0.1), harness.now());
        sprint(harness, 0.2, GROUND, 20);
        assertEquals(0, harness.candidates().size(), "Speed expiry while sprinting is lenient until applied");
        harness.acknowledge();
        harness.step(0.2 * 1.3 / (1 - GROUND), 0, 0, true);
        harness.step(0.2 * 1.3 / (1 - GROUND), 0, 0, true);
        run(harness, 0.2 * 1.3 / (1 - GROUND), 5, true);
        assertTrue(harness.candidates().size() >= 1, "faster than the applied attribute allows");
    }

    @Test
    void teleportsAndSneakingAndLegacyClientsAreSkipped() {
        CheckHarness teleported = harness();
        teleported.processor().serverTeleport(9, 100, 64, 0, 0, 0, false, false, false, false, false, 0, 0, 0,
                teleported.now());
        run(teleported, 0.8, 5, true);
        assertEquals(0, teleported.candidates().size());

        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new)
                .join().stand(45);
        run(legacy, 0.8, 10, true);
        assertEquals(0, legacy.candidates().size());
    }

    @Test
    void disabledChecksDoNothing() {
        CheckHarness harness = harness().settings(CheckCatalog.MOVEMENT_SPEED, new CheckSettings(false, Map.of()));
        run(harness, 0.8, 10, true);
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void notWarmedUpPlayersAreSkipped() {
        CheckHarness harness = CheckHarness.of(CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new).join().stand(10);
        run(harness, 0.8, 10, true);
        assertEquals(0, harness.candidates().size());
    }
}
