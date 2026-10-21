package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class InventoryClosedCheckTest {

    private static final long SECOND = 1_000_000_000L;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.INVENTORY_CLOSED, InventoryClosedCheck::new).join().stand(2);
    }

    private static void click(CheckHarness harness, int windowId) {
        harness.processor().windowClick(windowId, 10, 0, 0, harness.now());
    }

    private static void open(CheckHarness harness, int windowId) {
        harness.processor().openWindow(windowId, "minecraft:generic_9x3", harness.now());
    }

    @Test
    void clicksInTheOpenContainerAndThePlayerInventoryAreLegit() {
        CheckHarness harness = harness();
        click(harness, 0);
        open(harness, 3);
        for (int i = 0; i < 20; i++) {
            click(harness, 3);
            click(harness, 0);
            harness.tickEnd();
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void guiPageSwitchingIsLegitWhileTheClientHasNotAppliedTheReplacement() {
        CheckHarness harness = harness();
        open(harness, 3);
        harness.acknowledge();
        click(harness, 3);
        open(harness, 4);
        click(harness, 3);
        harness.tickEnd();
        click(harness, 3);
        click(harness, 4);
        open(harness, 4);
        click(harness, 4);
        assertEquals(0, harness.flags().size(), "clicks in flight for the replaced page");

        harness.acknowledge();
        click(harness, 3);
        assertEquals(1, harness.flags().size(), "the client applied the replacement");
        assertTrue(harness.flags().get(0).violation().info().contains("closed container 3"));
    }

    @Test
    void horseInventoriesAreContainersToo() {
        CheckHarness harness = harness();
        harness.processor().openWindow(7, "minecraft:horse", harness.now());
        harness.acknowledge();
        for (int i = 0; i < 10; i++) {
            click(harness, 7);
            harness.tickEnd();
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void clicksInAServerClosedContainerFlagOnceTheCloseIsAppliedOrTheGraceIsOver() {
        CheckHarness acknowledged = harness();
        open(acknowledged, 5);
        acknowledged.processor().serverCloseWindow(5, acknowledged.now());
        click(acknowledged, 5);
        assertEquals(0, acknowledged.flags().size());
        acknowledged.acknowledge();
        click(acknowledged, 5);
        assertEquals(1, acknowledged.flags().size());

        CheckHarness fallback = harness();
        open(fallback, 5);
        fallback.processor().serverCloseWindow(5, fallback.now());
        fallback.advance(200_000_000L);
        click(fallback, 5);
        assertEquals(0, fallback.flags().size(), "within 1.5 * ping (1 ms in the harness) + 250 ms");
        fallback.advance(100_000_000L);
        click(fallback, 5);
        assertEquals(1, fallback.flags().size());
    }

    @Test
    void clicksAfterTheClientClosedTheContainerFlag() {
        CheckHarness harness = harness();
        open(harness, 5);
        click(harness, 5);
        harness.processor().clientCloseWindow(5, harness.now());
        click(harness, 5);
        assertEquals(1, harness.flags().size());
    }

    @Test
    void containersThatWereNeverOpenedFlagAtMostOncePerSecond() {
        CheckHarness harness = harness();
        open(harness, 2);
        harness.acknowledge();
        harness.processor().clientCloseWindow(2, harness.now());
        click(harness, 9);
        click(harness, 10);
        assertEquals(1, harness.flags().size());
        assertTrue(harness.flags().get(0).violation().info().contains("never opened"));
        harness.advance(SECOND);
        click(harness, 11);
        assertEquals(2, harness.flags().size());
    }

    @Test
    void containersAreUnknownAfterJoinRespawnAndHotLoadUntilTheClientAppliedAnOpenOrClose() {
        CheckHarness joined = harness();
        click(joined, 9);
        assertEquals(0, joined.flags().size(), "unknown after JOIN_GAME");

        CheckHarness respawned = harness();
        open(respawned, 5);
        respawned.acknowledge();
        respawned.processor().respawn(GameMode.GAME_MODE_SURVIVAL, "minecraft:the_nether", 0, respawned.now());
        click(respawned, 5);
        open(respawned, 6);
        click(respawned, 5);
        click(respawned, 6);
        assertEquals(0, respawned.flags().size(), "a click from before the respawn may still be in flight");
        respawned.acknowledge();
        click(respawned, 5);
        assertEquals(1, respawned.flags().size(), "the client applied the new window");

        CheckHarness unseeded = CheckHarness.of(CheckCatalog.INVENTORY_CLOSED, InventoryClosedCheck::new);
        click(unseeded, 4);
        assertEquals(0, unseeded.flags().size());
    }
}
