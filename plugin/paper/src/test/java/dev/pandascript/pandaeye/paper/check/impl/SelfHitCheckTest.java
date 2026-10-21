package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SelfHitCheckTest {

    /** The harness joins with entity id 1. */
    private static final int OWN = 1;

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_SELFHIT, SelfHitCheck::new).join();
    }

    @Test
    void hittingOthersIsLegit() {
        CheckHarness harness = harness();
        for (int i = 2; i < 30; i++) {
            harness.processor().interact(i, i % 2 == 0, harness.now());
            harness.tickEnd();
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void attackingOrInteractingWithItselfFlags() {
        CheckHarness attack = harness();
        attack.processor().interact(OWN, true, attack.now());
        attack.processor().interact(OWN, true, attack.now());
        assertEquals(1, attack.flags().size(), "at most one flag per second");
        assertTrue(attack.flags().get(0).violation().info().contains("attacked"));
        assertEquals(1.0, attack.flags().get(0).violation().severity(), 1e-9);

        CheckHarness interact = harness();
        interact.processor().interact(OWN, false, interact.now());
        assertEquals(1, interact.flags().size());
    }

    @Test
    void anUnknownOwnEntityIsSkipped() {
        CheckHarness unseeded = CheckHarness.of(CheckCatalog.PROTOCOL_SELFHIT, SelfHitCheck::new);
        unseeded.processor().interact(-1, true, unseeded.now());
        unseeded.processor().interact(OWN, true, unseeded.now());
        assertEquals(0, unseeded.flags().size());
    }
}
