package dev.pandascript.pandaeye.common.litebans;

import dev.pandascript.pandaeye.common.litebans.BanClassifier.Label;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The label table of ARCHITECTURE 4.12, with reason strings seen on a live network. */
class BanClassifierTest {

    static final String STAFF = "61699b2e-d327-4a01-9f1e-0ea8c3f06bc6";
    static final String PLAYER = "069a79f4-44e9-4726-a5be-fca90e38aaf5";

    private final BanClassifier classifier = new BanClassifier(LiteBansSettings.DEFAULT);

    static BanEntry ban(String type, String reason, String executor, String removalReason) {
        return new BanEntry(42, type, PLAYER, reason, executor, removalReason, 1_790_000_000_000L, -1);
    }

    @ParameterizedTest(name = "{0} by {1} -> {2}")
    @CsvSource(delimiter = '|', nullValues = "NULL", value = {
            // staff bans for cheating
            "hacking / blacklisted modifications       | staff   | CHEAT",
            "cheating                                  | staff   | CHEAT",
            "Cheating                                  | staff   | CHEAT",
            "auto totem                                | staff   | CHEAT",
            "autototem                                 | staff   | CHEAT",
            "blacklisted modifications (easyplace)     | staff   | CHEAT",
            "easy place                                | staff   | CHEAT",
            "macros and scripts                        | staff   | CHEAT",
            "auto clicker                              | staff   | CHEAT",
            "KILLAURA                                  | staff   | CHEAT",
            "hacked client                             | staff   | CHEAT",
            // another anticheat's automatic bans run from the console: not counted by default
            "cheating (automatic detection)            | console | NONE",
            "cheating (automatic detection)            | CONSOLE | NONE",
            // not cheating, or not something PandaEye detects (ignore wins over a cheat reason)
            "bot account (spam joins)                  | staff   | NONE",
            "x-ray                                     | staff   | NONE",
            "esp/x-ray                                 | staff   | NONE",
            "xray and fly                              | staff   | NONE",
            "ban evading with an alt                   | staff   | NONE",
            "griefing                                  | staff   | NONE",
            "NULL                                      | staff   | NONE",
    })
    void bansAdded(String reason, String executor, Label expected) {
        String issuer = executor.equals("staff") ? STAFF : executor;
        assertEquals(expected, classifier.added(ban("ban", reason, issuer, null)));
    }

    @ParameterizedTest(name = "{0} lifted with {1} -> {2}")
    @CsvSource(delimiter = '|', nullValues = "NULL", value = {
            "cheating                         | false ban                       | LEGIT",
            "hacking / blacklisted modifications | appeal accepted (ticket #1234) | LEGIT",
            "auto clicker                     | Falsely banned, sorry           | LEGIT",
            "cheating                         | wrongful ban                    | LEGIT",
            "macros and scripts               | staff mistake                   | LEGIT",
            // no evidence either way
            "cheating                         | ban has been lifted.            | NONE",
            "cheating                         | unban wave 2026-10-27           | NONE",
            "cheating                         | NULL                            | NONE",
            "cheating                         | ''                              | NONE",
            // the original ban was not a cheating ban
            "x-ray                            | false ban                       | NONE",
            "bot account (spam joins)         | false ban                       | NONE",
            "griefing                         | appeal accepted (ticket #7)     | NONE",
    })
    void bansRemoved(String reason, String removal, Label expected) {
        assertEquals(expected, classifier.removed(ban("ban", reason, STAFF, removal)));
    }

    @Test
    void aWrongConsoleBanLiftedAsFalseIsLegitWhoeverIssuedIt() {
        assertEquals(Label.LEGIT, classifier.removed(ban("ban", "cheating (automatic detection)", "CONSOLE",
                "false ban")));
        assertEquals(Label.NONE, classifier.removed(ban("ban", "cheating (automatic detection)", "CONSOLE",
                "unban wave 2026-10-27")));
    }

    @Test
    void consoleBansCountOnlyWithCountConsole() {
        BanClassifier counting = new BanClassifier(new LiteBansSettings(true, LiteBansSettings.DEFAULT_CHEAT_REASONS,
                LiteBansSettings.DEFAULT_IGNORE_REASONS, LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS, true, 30));
        assertEquals(Label.CHEAT, counting.added(ban("ban", "cheating (automatic detection)", "CONSOLE", null)));
        assertEquals(Label.NONE, counting.added(ban("ban", "bot account (spam joins)", "CONSOLE", null)));
        assertEquals(Label.NONE, classifier.added(ban("ban", "cheating (automatic detection)", "CONSOLE", null)));
    }

    @Test
    void onlyBansCount() {
        for (String type : List.of("mute", "warn", "kick", "")) {
            assertEquals(Label.NONE, classifier.added(ban(type, "cheating", STAFF, null)), type);
            assertEquals(Label.NONE, classifier.removed(ban(type, "cheating", STAFF, "false ban")), type);
        }
        assertEquals(Label.NONE, classifier.added(ban(null, "cheating", STAFF, null)));
        assertEquals(Label.CHEAT, classifier.added(ban("BAN", "cheating", STAFF, null)));
    }

    @Test
    void consoleIsAnythingButAPlayersUuid() {
        for (String executor : new String[]{null, "", " ", "CONSOLE", "console", "[Console]", "#offline#",
                "00000000-0000-0000-0000-000000000000"}) {
            assertTrue(BanClassifier.console(executor), String.valueOf(executor));
        }
        assertFalse(BanClassifier.console(STAFF));
        assertFalse(BanClassifier.console("00000000-0000-0000-0009-01f2a3b4c5d6"), "a Bedrock (Floodgate) staff member");
    }

    @Test
    void configuredListsAreMatchedCaseInsensitivelyAndAnEmptyListMatchesNothing() {
        LiteBansSettings custom = new LiteBansSettings(true, List.of("Ghost Client", " "), List.of("TEST"),
                List.of("Appeal Accepted"), false, 30);
        assertEquals(List.of("ghost client"), custom.cheatReasons());
        BanClassifier classifier = new BanClassifier(custom);
        assertEquals(Label.CHEAT, classifier.added(ban("ban", "using a GHOST CLIENT", STAFF, null)));
        assertEquals(Label.NONE, classifier.added(ban("ban", "cheating", STAFF, null)), "not in the custom list");
        assertEquals(Label.NONE, classifier.added(ban("ban", "ghost client test", STAFF, null)), "ignore wins");
        assertEquals(Label.LEGIT, classifier.removed(ban("ban", "ghost client", STAFF, "appeal accepted")));

        BanClassifier none = new BanClassifier(new LiteBansSettings(true, List.of(), List.of(), List.of(), true, 30));
        assertEquals(Label.NONE, none.added(ban("ban", "cheating", STAFF, null)));
        assertEquals(Label.NONE, none.removed(ban("ban", "cheating", STAFF, "false ban")));
    }

    @Test
    void backfillDaysAreBounded() {
        assertEquals(0, new LiteBansSettings(true, null, null, null, false, -5).backfillDays());
        assertEquals(LiteBansSettings.MAX_BACKFILL_DAYS,
                new LiteBansSettings(true, null, null, null, false, 100_000).backfillDays());
    }
}
