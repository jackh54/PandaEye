package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import java.util.Random;
import java.util.function.IntToDoubleFunction;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class TimerBalanceCheckTest {

    private static final int FIVE_MINUTES = 6_000;

    @Test
    void jitterNeverFlags() {
        Random random = new Random(1);
        double[] delays = new double[FIVE_MINUTES];
        for (int i = 0; i < delays.length; i++) {
            delays[i] = 30 + random.nextDouble() * 40;
        }
        assertEquals(0, run(1.0, FIVE_MINUTES, k -> delays[k]).flags().size());
    }

    @Test
    void lagSpikesAndCatchUpNeverFlag() {
        // every 30 s the connection freezes for 1.5 s; packets then arrive in a burst
        CheckHarness harness = run(1.0, FIVE_MINUTES, k -> {
            double t = k * 50.0;
            double phase = t % 30_000;
            return phase < 1_500 ? 30 + (1_500 - phase) : 30;
        });
        assertEquals(0, harness.flags().size());
    }

    @Test
    void clockDriftWithinAllowanceNeverFlags() {
        assertEquals(0, run(1.0004, FIVE_MINUTES, k -> 30).flags().size());
    }

    @Test
    void aOneOffNetworkFloorDropIsAStepNotATimer() {
        assertEquals(0, run(1.0, FIVE_MINUTES, k -> k < 1_800 ? 130 : 30).flags().size());
    }

    @Test
    void aSingle400MsFloorDropNeverFlags() {
        assertEquals(0, run(1.0, FIVE_MINUTES, k -> k < 2_400 ? 430 : 30).flags().size());
        assertEquals(0, run(1.0, FIVE_MINUTES, k -> k < 1_300 ? 450 + (k % 7) : 50 + (k % 7)).flags().size());
    }

    @Test
    void driftAllowanceIsMeasuredFromWhenTheRunningMinimumWasSet() {
        // 0.45 ms/s of drift: under the 0.5 ms/s allowance however long it runs.
        assertEquals(0, run(1.00045, 12_000, k -> 30).flags().size());
    }

    @Test
    void timer101FlagsAfterCalibrationAndOneWindow() {
        CheckHarness harness = run(1.01, 2_400, k -> 30);
        assertTrue(harness.flags().size() >= 1, "1.01x must flag within two minutes");
        double firstFlagSeconds = secondsOfFirstFlag(1.01);
        assertTrue(firstFlagSeconds >= 70 && firstFlagSeconds <= 100, "flagged after " + firstFlagSeconds + " s");
        assertEquals(CheckCatalog.TIMER_BALANCE, harness.flags().get(0).check());
        assertTrue(harness.flags().get(0).violation().data().get("fall_ms") >= 150);
    }

    @Test
    void timer105FlagsRightAfterTheFirstWindow() {
        double seconds = secondsOfFirstFlag(1.05);
        assertTrue(seconds >= 60 && seconds <= 85, "flagged after " + seconds + " s");
    }

    @Test
    void countingRestartsAfterAViolation() {
        CheckHarness harness = run(1.05, 2_400, k -> 30);
        assertTrue(harness.flags().size() >= 2);
        assertTrue(harness.flags().get(1).vl() > harness.flags().get(0).vl());
    }

    @Test
    void stallsExcuseBins() {
        CheckHarness harness = CheckHarness.of(CheckCatalog.TIMER_BALANCE, TimerBalanceCheck::new).join();
        harness.signals().stall(0, Long.MAX_VALUE);
        feed(harness, 1.05, 2_400, k -> 30);
        assertEquals(0, harness.flags().size());
    }

    @Test
    void legacyClientsAreNotJudged() {
        CheckHarness harness = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.TIMER_BALANCE,
                TimerBalanceCheck::new).join();
        feed(harness, 1.10, 2_400, k -> 30);
        assertEquals(0, harness.flags().size());
    }

    @Test
    void abnormalTickRatesReset() {
        CheckHarness harness = CheckHarness.of(CheckCatalog.TIMER_BALANCE, TimerBalanceCheck::new).join();
        harness.processor().tickingState(10, false, harness.now());
        feed(harness, 1.05, 2_400, k -> 30);
        assertEquals(0, harness.flags().size());
    }

    private static double secondsOfFirstFlag(double speed) {
        CheckHarness harness = CheckHarness.of(CheckCatalog.TIMER_BALANCE, TimerBalanceCheck::new).join();
        long base = harness.now();
        for (int k = 0; k < 3_000; k++) {
            harness.processor().tickEnd(base + (long) ((k * 50 / speed + 30) * 1e6));
            if (!harness.flags().isEmpty()) {
                return k * 50 / speed / 1_000;
            }
        }
        return Double.POSITIVE_INFINITY;
    }

    private static CheckHarness run(double speed, int ticks, IntToDoubleFunction delayMs) {
        CheckHarness harness = CheckHarness.of(CheckCatalog.TIMER_BALANCE, TimerBalanceCheck::new).join();
        feed(harness, speed, ticks, delayMs);
        return harness;
    }

    /** Client tick k leaves at {@code k * 50 / speed} ms and arrives {@code delay(k)} later (in order). */
    private static void feed(CheckHarness harness, double speed, int ticks, IntToDoubleFunction delayMs) {
        long base = harness.now();
        double last = 0;
        for (int k = 0; k < ticks; k++) {
            double arrival = Math.max(last, k * 50 / speed + delayMs.applyAsDouble(k));
            last = arrival;
            harness.processor().tickEnd(base + (long) (arrival * 1e6));
        }
    }
}
