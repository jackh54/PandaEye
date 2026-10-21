package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class FloodCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_FLOOD, FloodCheck::new).join().stand(5);
    }

    /** One tick with {@code packets} movement packets. */
    private static void tick(CheckHarness harness, int packets) {
        for (int i = 0; i < packets; i++) {
            harness.processor().flying(0, 64, 0, 0, 0, true, false, true, false, false, harness.now());
        }
        harness.tickEnd();
    }

    @Test
    void onePacketPerTickAndIdleTicksAreLegit() {
        CheckHarness harness = harness();
        for (int i = 0; i < 200; i++) {
            tick(harness, i % 3 == 0 ? 0 : 1);
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void teleportsConfirmedInTheSameTickAreAllowed() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            harness.processor().serverTeleport(i, 0, 64, 0, 0, 0, false, false, false, false, false, 0, 0, 0,
                    harness.now());
            harness.processor().serverTeleport(100 + i, 0, 70, 0, 0, 0, false, false, false, false, false, 0, 0, 0,
                    harness.now());
            harness.processor().teleportConfirm(i, harness.now());
            harness.processor().flying(0, 64, 0, 0, 0, true, true, false, false, false, harness.now());
            harness.processor().teleportConfirm(100 + i, harness.now());
            harness.processor().flying(0, 70, 0, 0, 0, true, true, false, false, false, harness.now());
            tick(harness, 1);
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void serverRotationsAreAllowedAnExtraRotationPacket() {
        CheckHarness harness = harness();
        for (int i = 0; i < 30; i++) {
            harness.processor().serverRotation(10, false, true);
            harness.processor().flying(0, 0, 0, 90, 10, false, true, true, false, false, harness.now());
            tick(harness, 1);
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void threeFloodedTicksWithinTwentyFlagAndRestartTheWindow() {
        CheckHarness harness = harness();
        tick(harness, 2);
        tick(harness, 1);
        tick(harness, 3);
        assertEquals(0, harness.flags().size());
        for (int i = 0; i < 10; i++) {
            tick(harness, 1);
        }
        tick(harness, 2);
        assertEquals(1, harness.flags().size());
        CheckHarness.Flag flag = harness.flags().get(0);
        assertEquals(3, flag.violation().data().get("excess_ticks"), 1e-9);
        assertTrue(flag.violation().info().contains("2 > 1"));

        harness.advance(1_000_000_000L);
        tick(harness, 2);
        tick(harness, 2);
        assertEquals(1, harness.flags().size(), "the window restarted after the violation");
        tick(harness, 2);
        assertEquals(2, harness.flags().size());
    }

    @Test
    void floodedTicksSpreadBeyondTwentyTicksDoNotFlag() {
        CheckHarness harness = harness();
        for (int i = 0; i < 10; i++) {
            tick(harness, 2);
            for (int j = 0; j < 10; j++) {
                tick(harness, 1);
            }
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void legacyClientsAndTheTickAfterAJoinAreNotJudged() {
        CheckHarness legacy = CheckHarness.of(CheckHarness.LEGACY_CLIENT, CheckCatalog.PROTOCOL_FLOOD, FloodCheck::new)
                .join();
        for (int i = 0; i < 10; i++) {
            tick(legacy, 4);
        }
        assertEquals(0, legacy.flags().size());

        CheckHarness rejoined = harness();
        tick(rejoined, 3);
        tick(rejoined, 3);
        rejoined.processor().joinGame(1, dev.pandascript.pandaeye.protocol.v1.GameMode.GAME_MODE_SURVIVAL,
                "minecraft:overworld", null, rejoined.now());
        tick(rejoined, 3);
        tick(rejoined, 1);
        assertEquals(0, rejoined.flags().size());
    }
}
