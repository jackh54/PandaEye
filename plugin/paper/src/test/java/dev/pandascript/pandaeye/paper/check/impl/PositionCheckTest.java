package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PositionCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_POSITION, PositionCheck::new).join();
    }

    private static void flying(CheckHarness harness, double x, double y, double z, float yaw, float pitch,
                               boolean hasPos, boolean hasRot) {
        harness.processor().flying(x, y, z, yaw, pitch, hasPos, hasRot, true, false, false, harness.now());
        harness.tickEnd();
    }

    @Test
    void positionsInsideTheWorldAndRotationOnlyPacketsAreLegit() {
        CheckHarness harness = harness();
        flying(harness, 29_999_999.5, 64, -29_999_999.5, 0, 0, true, false);
        flying(harness, 0, -2_000, 0, 720, 90, true, true);
        flying(harness, 0, 0, 0, -1_000_000, 0, false, true);
        flying(harness, 0, 0, 0, 0, 0, false, false);
        harness.stand(10);
        assertEquals(0, harness.flags().size());
    }

    @Test
    void notFiniteValuesFlag() {
        for (double[] bad : new double[][]{{Double.NaN, 64, 0}, {0, Double.POSITIVE_INFINITY, 0},
                {0, 64, Double.NEGATIVE_INFINITY}}) {
            CheckHarness harness = harness();
            flying(harness, bad[0], bad[1], bad[2], 0, 0, true, false);
            assertEquals(1, harness.flags().size());
            assertEquals(CheckCatalog.PROTOCOL_POSITION, harness.flags().get(0).check());
            assertEquals(1.0, harness.flags().get(0).violation().severity(), 1e-9);
        }
        CheckHarness yaw = harness();
        flying(yaw, 0, 0, 0, Float.NaN, 0, false, true);
        flying(yaw, 0, 0, 0, 0, Float.POSITIVE_INFINITY, false, true);
        assertEquals(1, yaw.flags().size(), "at most one flag per second");
        assertTrue(yaw.flags().get(0).violation().info().contains("NaN"));
    }

    @Test
    void positionsBeyondTheWorldLimitsFlagAtMostOncePerSecond() {
        CheckHarness harness = harness();
        flying(harness, 30_000_001, 64, 0, 0, 0, true, false);
        flying(harness, 0, 64, -30_000_001, 0, 0, true, false);
        flying(harness, 0, 20_000_001, 0, 0, 0, true, false);
        assertEquals(1, harness.flags().size());
        harness.advance(1_000_000_000L);
        flying(harness, 0, -20_000_001, 0, 0, 0, true, false);
        assertEquals(2, harness.flags().size());
        assertEquals(2, harness.flags().get(1).vl(), 0.01);
    }
}
