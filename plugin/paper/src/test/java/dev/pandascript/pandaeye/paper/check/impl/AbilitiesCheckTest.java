package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckHarness;
import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class AbilitiesCheckTest {

    private static CheckHarness harness() {
        return CheckHarness.of(CheckCatalog.PROTOCOL_ABILITIES, AbilitiesCheck::new).join().stand(5);
    }

    @Test
    void flyingWithAllowFlightOrInCreativeIsLegit() {
        CheckHarness allowed = harness();
        allowed.processor().serverAbilities(new Abilities(false, true, false, 0.05f), allowed.now());
        allowed.processor().clientAbilities(true, allowed.now());
        allowed.processor().clientAbilities(false, allowed.now());
        assertEquals(0, allowed.flags().size());

        CheckHarness creative = harness();
        creative.processor().gameMode(GameMode.GAME_MODE_CREATIVE, creative.now());
        creative.processor().clientAbilities(true, creative.now());
        assertEquals(0, creative.flags().size());
    }

    @Test
    void flyingWithoutPermissionFlagsAtMostOncePerSecond() {
        CheckHarness harness = harness();
        harness.processor().clientAbilities(false, harness.now());
        harness.processor().clientAbilities(true, harness.now());
        harness.processor().clientAbilities(true, harness.now());
        assertEquals(1, harness.flags().size());
        assertEquals(CheckCatalog.PROTOCOL_ABILITIES, harness.flags().get(0).check());
        harness.advance(1_000_000_000L);
        harness.processor().clientAbilities(true, harness.now());
        assertEquals(2, harness.flags().size());
    }

    @Test
    void aRevokedFlightCountsUntilAppliedAndForASecondAfter() {
        CheckHarness harness = harness();
        harness.processor().serverAbilities(new Abilities(true, true, false, 0.05f), harness.now());
        harness.acknowledge();
        harness.stand(2);
        harness.processor().serverAbilities(new Abilities(false, false, false, 0.05f), harness.now());
        harness.processor().clientAbilities(true, harness.now());
        harness.acknowledge();
        harness.stand(2);
        harness.processor().clientAbilities(true, harness.now());
        assertEquals(0, harness.flags().size(), "toggled before or right after the revocation was applied");
        harness.stand(25);
        harness.processor().clientAbilities(true, harness.now());
        assertEquals(1, harness.flags().size());
    }

    @Test
    void creativeEndingIsLenientUntilApplied() {
        CheckHarness harness = harness();
        harness.processor().gameMode(GameMode.GAME_MODE_CREATIVE, harness.now());
        harness.acknowledge();
        harness.stand(2);
        harness.processor().gameMode(GameMode.GAME_MODE_SURVIVAL, harness.now());
        harness.processor().clientAbilities(true, harness.now());
        assertEquals(0, harness.flags().size());
    }
}
