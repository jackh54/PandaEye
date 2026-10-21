package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class CursorCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_CURSOR, CursorCheck::new).join();
    }

    private static void place(CheckHarness harness, float x, float y, float z) {
        harness.processor().place(0, 64, 0, 1, x, y, z, 0, false, harness.now());
        harness.tickEnd();
    }

    @Test
    void cursorsOnTheBlockAreLegit() {
        CheckHarness harness = harness();
        place(harness, 0, 0, 0);
        place(harness, 1, 1, 1);
        place(harness, 0.5f, 1, 0.25f);
        place(harness, 1.0009f, -0.0009f, 0.9375f);
        // Seen from legit players on a live server: shapes larger than a block, accepted by vanilla.
        place(harness, 0.44810194f, 1.1f, 0.24138473f);
        place(harness, 0.30169216f, 1.5f, 0.6139399f);
        place(harness, 0.63209695f, 0.7903465f, -0.099999964f);
        place(harness, -0.5f, 1.5f, 1.5f);
        assertEquals(0, harness.flags().size());
    }

    @Test
    void cursorsOffTheBlockOrNaNFlagAtMostOncePerSecond() {
        CheckHarness harness = harness();
        place(harness, 1.51f, 0.5f, 0.5f);
        place(harness, Float.NaN, 0.5f, 0.5f);
        assertEquals(1, harness.flags().size());
        assertEquals(1.51f, harness.flags().get(0).violation().data().get("cursor_x"), 1e-6);
        harness.advance(1_000_000_000L);
        place(harness, 0.5f, -3, 0.5f);
        assertEquals(2, harness.flags().size());
    }
}
