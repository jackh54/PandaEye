package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.DigProbe;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class DigSpeedCheckTest {

    private static final int X = 3;
    private static final int Y = 63;
    private static final int Z = -2;
    /** Stone (hardness 1.5) with a diamond pickaxe (speed 8): 6 ticks. */
    private static final float STONE_DIAMOND = 8f / 1.5f / 30f;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.DIG_SPEED, DigSpeedCheck::new).join().stand(45);
    }

    private static DigProbe probe(CheckHarness harness, float speed, boolean onGround, boolean underWater, int boost,
                                  double efficiency) {
        return new DigProbe(X, Y, Z, speed, speed >= 1, onGround, underWater, boost, -1, efficiency, 1.0, 0.2,
                harness.now());
    }

    /** START, the region thread's probe, {@code ticks} client ticks, FINISH. */
    private static void dig(CheckHarness harness, DigProbe probe, int ticks) {
        harness.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, harness.now());
        harness.signals().digProbe(probe);
        harness.stand(ticks);
        harness.processor().dig(CheckInput.DigAction.FINISH, X, Y, Z, 1, harness.now());
        harness.stand(1);
    }

    @Test
    void vanillaDigsByClickAndByHoldingAreLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 20; i++) {
            dig(harness, probe(harness, STONE_DIAMOND, true, false, -1, 0), 5);
            dig(harness, probe(harness, STONE_DIAMOND, true, false, -1, 0), 6);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void fastBreakIsACandidateThatConfirms() {
        CheckHarness harness = harness();
        dig(harness, probe(harness, STONE_DIAMOND, true, false, -1, 0), 2);
        assertEquals(1, harness.candidates().size());
        harness.confirmAll();
        CheckHarness.Flag flag = harness.flags().get(0);
        assertEquals(CheckCatalog.DIG_SPEED, flag.check());
        assertEquals(6, flag.violation().data().get("expected"), 1e-9);
        assertEquals(2, flag.violation().data().get("ticks"), 1e-9);
        assertEquals(1 - 2 / 6.0, flag.violation().severity(), 1e-9);
        assertEquals(1, flag.violation().vlIncrement());
    }

    @Test
    void hasteAndEfficiencyToolsAreJudgedWithTheServersSpeed() {
        CheckHarness harness = harness();
        harness.processor().effect(EffectTracker.HASTE, 1, harness.now());
        harness.processor().attributes(Map.of(AttributeTracker.MINING_EFFICIENCY, 5.0), harness.now());
        harness.acknowledge();
        harness.stand(1);
        // deepslate (hardness 3), iron pickaxe (6) with efficiency II (+5), haste II (x1.4): 6 ticks
        float boosted = (6f + 5f) * 1.4f / 3f / 30f;
        for (int i = 0; i < 10; i++) {
            dig(harness, probe(harness, boosted, true, false, 1, 5), 5);
            dig(harness, probe(harness, boosted, true, false, 1, 5), 6);
        }
        assertEquals(0, harness.candidates().size());
        dig(harness, probe(harness, boosted, true, false, 1, 5), 2);
        assertEquals(1, harness.candidates().size(), "the same player is still judged");
    }

    @Test
    void instantBreakBlocksAreSkipped() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            dig(harness, probe(harness, 1.5f, true, false, -1, 0), 0);
            dig(harness, new DigProbe(X, Y, Z, 0.1f, true, true, false, -1, -1, 0, 1, 0.2, harness.now()), 0);
        }
        assertEquals(0, harness.candidates().size());
    }

    @Test
    void speedChangesDuringTheDigOrPendingAtTheClientSkip() {
        CheckHarness haste = harness();
        haste.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, haste.now());
        haste.signals().digProbe(probe(haste, STONE_DIAMOND, true, false, -1, 0));
        haste.stand(1);
        haste.processor().effect(EffectTracker.HASTE, 2, haste.now());
        haste.acknowledge();
        haste.stand(1);
        haste.processor().dig(CheckInput.DigAction.FINISH, X, Y, Z, 1, haste.now());
        haste.stand(1);
        assertEquals(0, haste.candidates().size(), "haste gained mid-dig");

        CheckHarness efficiency = harness();
        efficiency.processor().attributes(Map.of(AttributeTracker.MINING_EFFICIENCY, 26.0), efficiency.now());
        dig(efficiency, probe(efficiency, STONE_DIAMOND, true, false, -1, 0), 1);
        assertEquals(0, efficiency.candidates().size(), "tool switched, efficiency not applied yet");

        CheckHarness fatigue = harness();
        fatigue.processor().effect(EffectTracker.MINING_FATIGUE, 0, fatigue.now());
        fatigue.acknowledge();
        fatigue.stand(1);
        fatigue.processor().effectRemoved(EffectTracker.MINING_FATIGUE, fatigue.now());
        dig(fatigue, probe(fatigue, STONE_DIAMOND * 0.3f, true, false, -1, 0), 2);
        assertEquals(0, fatigue.candidates().size(), "mining fatigue removal pending");
    }

    @Test
    void effectsTheServerDoesNotKnowSkip() {
        CheckHarness harness = harness();
        harness.processor().effect(EffectTracker.HASTE, 4, harness.now());
        harness.acknowledge();
        harness.stand(1);
        dig(harness, probe(harness, STONE_DIAMOND, true, false, -1, 0), 1);
        assertEquals(0, harness.candidates().size(), "a fake haste packet makes the client faster than the server");
    }

    @Test
    void startingInTheAirOrUnderWaterIsLenient() {
        CheckHarness air = harness();
        for (int i = 0; i < 5; i++) {
            dig(air, probe(air, STONE_DIAMOND / 5, false, false, -1, 0), 5);
        }
        assertEquals(0, air.candidates().size(), "the client may land during the dig");

        CheckHarness water = harness();
        for (int i = 0; i < 5; i++) {
            dig(water, probe(water, STONE_DIAMOND * 0.2f, true, true, -1, 0), 5);
        }
        assertEquals(0, water.candidates().size(), "the client may leave the water during the dig");
    }

    @Test
    void aNukerWhoseFinishArrivesBeforeTheProbeIsStillJudged() {
        CheckHarness harness = harness();
        harness.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, harness.now());
        harness.processor().dig(CheckInput.DigAction.FINISH, X, Y, Z, 1, harness.now());
        assertEquals(0, harness.candidates().size());
        harness.signals().digProbe(probe(harness, STONE_DIAMOND, true, false, -1, 0));
        harness.stand(1);
        assertEquals(1, harness.candidates().size());
    }

    @Test
    void finishesWithoutProbeOrMatchingStartAreSkipped() {
        CheckHarness noProbe = harness();
        noProbe.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, noProbe.now());
        noProbe.processor().dig(CheckInput.DigAction.FINISH, X, Y, Z, 1, noProbe.now());
        noProbe.stand(30);
        noProbe.signals().digProbe(probe(noProbe, STONE_DIAMOND, true, false, -1, 0));
        noProbe.stand(1);
        assertEquals(0, noProbe.candidates().size(), "a probe that never came within a second");

        CheckHarness other = harness();
        other.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, other.now());
        other.signals().digProbe(probe(other, STONE_DIAMOND, true, false, -1, 0));
        other.processor().dig(CheckInput.DigAction.FINISH, X + 1, Y, Z, 1, other.now());
        other.processor().dig(CheckInput.DigAction.START, X, Y, Z, 1, other.now());
        other.processor().dig(CheckInput.DigAction.CANCEL, X, Y, Z, 1, other.now());
        other.processor().dig(CheckInput.DigAction.FINISH, X, Y, Z, 1, other.now());
        other.stand(1);
        assertEquals(0, other.candidates().size());
    }

    @Test
    void creativeAndLegacyClientsAreSkipped() {
        CheckHarness creative = harness();
        creative.processor().gameMode(GameMode.GAME_MODE_CREATIVE, creative.now());
        dig(creative, probe(creative, STONE_DIAMOND, true, false, -1, 0), 0);
        assertEquals(0, creative.candidates().size());

        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.DIG_SPEED, DigSpeedCheck::new)
                .join().stand(45);
        dig(legacy, probe(legacy, STONE_DIAMOND, true, false, -1, 0), 0);
        assertEquals(0, legacy.candidates().size());
        assertTrue(legacy.flags().isEmpty());
    }
}
