package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PitchCheckTest {

    private CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_PITCH, PitchCheck::new).join();
    }

    @Test
    void pitchWithinTheClampIsLegit() {
        CheckHarness harness = harness().look(0, 45).look(10, 90).look(20, -90).look(30, 90.0009f);
        assertEquals(0, harness.flags().size());
    }

    @Test
    void pitchBeyondTheClampFlagsAtMostOncePerSecond() {
        CheckHarness harness = harness().look(0, 90.5f).look(0, -91).look(0, 120);
        assertEquals(1, harness.flags().size());
        assertTrue(harness.flags().get(0).violation().info().contains("90.5"));
        harness.advance(1_000_000_000L).look(0, -91);
        assertEquals(2, harness.flags().size());
        assertEquals(2, harness.flags().get(1).vl(), 0.01);
    }

    @Test
    void serverSetOutOfRangePitchIsNotTheClientsFault() {
        CheckHarness harness = harness();
        harness.processor().serverTeleport(5, 0, 64, 0, 0, 100, false, false, false, false, false, 0, 0, 0,
                harness.now());
        harness.processor().teleportConfirm(5, harness.now());
        harness.processor().flying(0, 64, 0, 0, 100, true, true, false, false, false, harness.now());
        harness.tickEnd().look(0, 100).look(0, 100);
        assertEquals(0, harness.flags().size(), "out of range until the next server rotation");

        harness.processor().serverRotation(10, false, true);
        harness.look(0, 10).look(0, 100);
        assertEquals(1, harness.flags().size());
    }

    @Test
    void serverRotationMarksOnlyTheNextRotation() {
        CheckHarness harness = harness();
        harness.processor().serverRotation(0, true, false);
        harness.look(0, 95);
        assertEquals(0, harness.flags().size());
        harness.look(0, 95);
        assertEquals(1, harness.flags().size());
    }

    @Test
    void exemptAndUnseededPlayersAreNotJudged() {
        CheckHarness exempt = harness();
        exempt.signals().exempt(true);
        exempt.look(0, 120);
        assertEquals(0, exempt.flags().size());

        CheckHarness unseeded = CheckHarness.of(CheckCatalog.PROTOCOL_PITCH, PitchCheck::new);
        unseeded.look(0, 120);
        assertEquals(0, unseeded.flags().size());
    }
}
