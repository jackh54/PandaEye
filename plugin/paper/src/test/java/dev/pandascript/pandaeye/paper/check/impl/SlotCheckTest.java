package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class SlotCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_SLOT, SlotCheck::new).join();
    }

    @Test
    void normalSlotChangesAreLegit() {
        CheckHarness harness = harness();
        for (int i = 1; i <= 40; i++) {
            harness.processor().clientHeldItem(i % 9, harness.now());
            harness.tickEnd();
        }
        assertEquals(0, harness.flags().size());
    }

    @Test
    void echoesOfServerSetHeldItemAndPickBlockAreLegit() {
        CheckHarness harness = harness();
        harness.processor().serverHeldItem(4);
        harness.processor().clientHeldItem(4, harness.now());
        harness.tickEnd();
        // pick block: the server selects slot 7, the client echoes it
        harness.processor().serverHeldItem(7);
        harness.tickEnd();
        harness.processor().clientHeldItem(7, harness.now());
        // a server set to the slot already selected expects no echo
        harness.processor().serverHeldItem(7);
        harness.processor().clientHeldItem(2, harness.now());
        // two sets before the client answers: only the last is echoed
        harness.processor().serverHeldItem(5);
        harness.processor().serverHeldItem(6);
        harness.processor().clientHeldItem(6, harness.now());
        assertEquals(0, harness.flags().size());
    }

    @Test
    void slotsOutsideTheHotbarAreVariantRange() {
        CheckHarness harness = harness();
        harness.processor().clientHeldItem(9, harness.now());
        harness.processor().clientHeldItem(-1, harness.now());
        assertEquals(1, harness.flags().size(), "at most one flag per second");
        CheckHarness.Flag flag = harness.flags().get(0);
        assertEquals("range", flag.violation().variant());
        assertEquals(9, flag.violation().data().get("slot"), 1e-9);
        assertEquals(1, flag.violation().vlIncrement());
        harness.advance(1_000_000_000L);
        harness.processor().clientHeldItem(40, harness.now());
        assertEquals(2, harness.flags().size());
    }

    @Test
    void reselectingTheCurrentSlotIsVariantRedundantWithoutVl() {
        CheckHarness harness = harness();
        harness.processor().clientHeldItem(3, harness.now());
        harness.processor().clientHeldItem(3, harness.now());
        assertEquals(1, harness.flags().size());
        CheckHarness.Flag flag = harness.flags().get(0);
        assertEquals("redundant", flag.violation().variant());
        assertEquals(0, flag.violation().vlIncrement());
        assertEquals(0, flag.vl(), 1e-9);
        harness.processor().clientHeldItem(12, harness.now());
        assertEquals(2, harness.flags().size(), "variants are limited separately");
    }

    @Test
    void anUnknownMirrorIsNotJudged() {
        CheckHarness unseeded = CheckHarness.of(CheckCatalog.PROTOCOL_SLOT, SlotCheck::new);
        unseeded.processor().clientHeldItem(3, unseeded.now());
        unseeded.processor().clientHeldItem(3, unseeded.now());
        assertEquals(0, unseeded.flags().size());
    }
}
