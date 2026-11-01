package dev.pandascript.pandaeye.common.litebans;

import dev.pandascript.pandaeye.common.litebans.BanClassifier.Label;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.ValueSource;

import java.util.List;
import java.util.Locale;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The label table of ARCHITECTURE 4.12, with reason strings seen on a live network. */
class BanClassifierTest {

    static final String STAFF = "61699b2e-d327-4a01-9f1e-0ea8c3f06bc6";
    static final String PLAYER = "069a79f4-44e9-4726-a5be-fca90e38aaf5";

    private final BanClassifier classifier = new BanClassifier(LiteBansSettings.DEFAULT);
    private final BanClassifier countingConsole = new BanClassifier(new LiteBansSettings(true,
            LiteBansSettings.DEFAULT_CHEAT_REASONS, LiteBansSettings.DEFAULT_IGNORE_REASONS,
            LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS, true, 30));

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

    /** Why a production reason gives its label with the default lists. */
    enum Outcome {
        /** CHEAT when staff issued it. */
        CHEAT,
        /** NONE: an ignore-reason matches, whether or not a cheat-reason does. */
        IGNORED,
        /** NONE: no cheat-reason matches. */
        NO_CHEAT_REASON
    }

    /** Reasons from a production LiteBans database (names removed), banned by staff, with the default lists. */
    @ParameterizedTest(name = "{0} -> {1}")
    @CsvSource(delimiter = '|', value = {
            "Hacking / Blacklisted Modifications                | CHEAT",
            "Admitted Use of Hacks or Blacklisted Modifications | CHEAT",
            "Auto Totem                                         | CHEAT",
            "Cheating                                           | CHEAT",
            "easyplace                                          | CHEAT",
            "Blacklisted Modifications (easyplace)              | CHEAT",
            "hacking-invismod                                   | CHEAT",
            "macros/scripts                                     | CHEAT",
            "hacking/fly                                        | CHEAT",
            "Auto Clicker                                       | CHEAT",
            "easy place                                         | CHEAT",
            "/ blacklisted modifications (autototem)            | CHEAT",
            "/ blacklisted modifications                        | CHEAT",
            "(autototem)                                        | CHEAT",
            "cheating (autototem)                               | CHEAT",
            "macros and scripts                                 | CHEAT",
            "Cheating (automatic detection)                     | CHEAT",
            "KillAura                                           | CHEAT",
            "Hacked client                                      | CHEAT",
            "Fly hacks (disrespectful)                          | CHEAT",
            "Hacking (fastest bridging)                         | CHEAT",
            "Unfair advantage. Appeal at https://eye-dash.pandascript.dev/appeal with code AB12CD | CHEAT",
            "X-Ray                                              | IGNORED",
            "ESP                                                | IGNORED",
            "Hacking/X-Ray                                      | IGNORED",
            "esp/hacking                                        | IGNORED",
            "esp/x-ray                                          | IGNORED",
            "esp/piechart                                       | IGNORED",
            "Bot Account (mass join 2026-10-22)                 | IGNORED",
            "Ban/Mute Evading                                   | IGNORED",
            "testing                                            | IGNORED",
            "Hacking (alts: a, b)                               | IGNORED",
            "You have violated our community guidelines.        | NO_CHEAT_REASON",
            "Kill farming                                       | NO_CHEAT_REASON",
            "Ban Evading                                        | NO_CHEAT_REASON",
            "DDossing / Doxxing                                 | NO_CHEAT_REASON",
            "Bug Abuse                                          | NO_CHEAT_REASON",
            "Failing to SS                                      | NO_CHEAT_REASON",
            "pie chart(admitted                                 | NO_CHEAT_REASON",
            "Slurs                                              | NO_CHEAT_REASON",
            "Elo farming                                        | NO_CHEAT_REASON",
            "x-rau                                              | NO_CHEAT_REASON",
            "until proven otherwise.                            | NO_CHEAT_REASON",
            "pending                                            | NO_CHEAT_REASON",
            "'#30d'                                             | NO_CHEAT_REASON",
            "Breach of trade rules                              | NO_CHEAT_REASON",
            "Staff Disrespect                                   | NO_CHEAT_REASON",
            "IRL trading                                        | NO_CHEAT_REASON",
            "Abusing staff tools to raid bases                  | NO_CHEAT_REASON",
            "Description spam                                   | NO_CHEAT_REASON",
    })
    void productionReasonsWithTheDefaults(String reason, Outcome outcome) {
        Label label = classifier.added(ban("ban", reason, STAFF, null));
        assertEquals(outcome == Outcome.CHEAT ? Label.CHEAT : Label.NONE, label);
        if (outcome == Outcome.IGNORED) {
            assertTrue(BanClassifier.matches(reason, LiteBansSettings.DEFAULT_IGNORE_REASONS), "an ignore-reason");
        } else if (outcome == Outcome.NO_CHEAT_REASON) {
            assertFalse(BanClassifier.matches(reason, LiteBansSettings.DEFAULT_CHEAT_REASONS), "no cheat-reason");
        }
    }

    @ParameterizedTest
    @ValueSource(strings = {"Cheating (automatic detection)", "Hacking / Blacklisted Modifications"})
    void productionConsoleBansCountOnlyWithCountConsole(String reason) {
        assertEquals(Label.NONE, classifier.added(ban("ban", reason, "CONSOLE", null)));
        assertEquals(Label.CHEAT, countingConsole.added(ban("ban", reason, "CONSOLE", null)));
    }

    @ParameterizedTest(name = "lifted with {0} -> {1}")
    @CsvSource(delimiter = '|', value = {
            "false ban                           | LEGIT",
            "Appeal accepted (ticket #1023fm8q)  | LEGIT",
            "mistaken identity                   | LEGIT",
            "unban wave 2026-10-27               | NONE",
            "Ban has been lifted.                | NONE",
            // the removal reason LiteBans writes when a ban is updated
            "Hacking / Blacklisted Modifications | NONE",
    })
    void productionRemovalsWithTheDefaults(String removal, Label expected) {
        assertEquals(expected, classifier.removed(ban("ban", "Hacking / Blacklisted Modifications", STAFF, removal)));
    }

    /** Hyphens and underscores count as spaces, in the reason and in the entry. */
    @ParameterizedTest(name = "{0} -> {1}")
    @CsvSource(delimiter = '|', value = {
            "Anti-Knockback        | CHEAT",
            "Aim-bot               | CHEAT",
            "Auto-clicker          | CHEAT",
            "No-fall               | CHEAT",
            "kill_aura             | CHEAT",
            "Hacking (X Ray)       | NONE",
            "Hacking (ban evasion) | NONE",
            "hacking-alt           | NONE",
            "Breach-of-trust       | NONE",
    })
    void hyphensAndUnderscoresAreSpaces(String reason, Label expected) {
        assertEquals(expected, classifier.added(ban("ban", reason, STAFF, null)));
    }

    @ParameterizedTest(name = "lifted with {0} -> {1}")
    @CsvSource(delimiter = '|', value = {
            "false-ban                 | LEGIT",
            "False positive            | LEGIT",
            "Wrongly banned            | LEGIT",
            "Wrong ban                 | LEGIT",
            "Accepted appeal           | LEGIT",
            "Appeal approved (#ab12)   | LEGIT",
            "Appeal denied             | NONE",
            "Time served               | NONE",
    })
    void otherWaysStaffSayABanWasWrong(String removal, Label expected) {
        assertEquals(expected, classifier.removed(ban("ban", "Hacking / Blacklisted Modifications", STAFF, removal)));
    }

    /** A URL is not part of the reason: PandaEye's ban reason carries the appeal link, whatever the owner set. */
    @ParameterizedTest(name = "{0}")
    @ValueSource(strings = {
            "Unfair advantage. Appeal at https://appeal.esportsmc.net/appeal with code AB12CD",
            "Unfair advantage. Appeal at https://test.example/appeal with code AB12CD",
            "Unfair advantage. Appeal at http://alt-appeals.example with code AB12CD",
            "Unfair advantage. Appeal at www.xray-free.example/appeal with code AB12CD",
    })
    void urlsInTheReasonAreSkipped(String reason) {
        assertEquals(Label.LEGIT, classifier.removed(ban("ban", reason, "CONSOLE", "false ban")));
        assertEquals(Label.CHEAT, classifier.added(ban("ban", reason, STAFF, null)));
        assertFalse(BanClassifier.matches("see https://hack.example/x", List.of("hack")));
        assertTrue(BanClassifier.matches("see https://example.org/x, hacking", List.of("hack")), "text after the URL");
    }

    @Test
    void pandaEyesOwnConsoleBanIsOnlyLabelledWhenLiftedAsWrong() {
        String reason = "Unfair advantage. Appeal at https://eye-dash.pandascript.dev/appeal with code AB12CD";
        assertEquals(Label.NONE, classifier.added(ban("ban", reason, "CONSOLE", null)), "console bans are off");
        assertEquals(Label.LEGIT, classifier.removed(ban("ban", reason, "CONSOLE", "false ban")));
        assertEquals(Label.NONE, classifier.removed(ban("ban", reason, "CONSOLE", "Ban has been lifted.")));
        assertFalse(BanClassifier.matches("Appeal at https://eye-dash.pandascript.dev/appeal",
                LiteBansSettings.DEFAULT_CHEAT_REASONS), "\"script\" does not match inside the appeal URL");
    }

    @ParameterizedTest(name = "{1} in {0} -> {2}")
    @CsvSource(delimiter = '|', value = {
            // at the start, after a space, punctuation, a slash or a parenthesis; the end is free
            "reach                     | reach           | true",
            "Reach 4.2                 | reach           | true",
            "using reach               | reach           | true",
            "fly,reach                 | reach           | true",
            "fly.reach                 | reach           | true",
            "fly/reach                 | reach           | true",
            "(reach)                   | reach           | true",
            "-reach                    | reach           | true",
            "_reach                    | reach           | true",
            "\u00bbreach               | reach           | true",
            "reaching                  | reach           | true",
            "hacking-invismod          | hack            | true",
            "mistaken identity         | mistake         | true",
            "alts                      | alt             | true",
            // never after a letter or digit, other alphabets included
            "breach                    | reach           | false",
            "outreach                  | reach           | false",
            "4reach                    | reach           | false",
            "\u00e9reach               | reach           | false",
            "disrespect                | esp             | false",
            "halt                      | alt             | false",
            "fastest                   | test            | false",
            "pandascript.dev           | script          | false",
            "killaura                  | aura            | false",
            // every occurrence is tried, not only the first
            "breach, reach             | reach           | true",
            "breach outreach           | reach           | false",
            "killaura aura             | aura            | true",
            // entries with a space or a hyphen
            "Blacklisted Modifications | blacklisted mod | true",
            "unblacklisted mods        | blacklisted mod | false",
            "(anti-kb)                 | anti-kb         | true",
            "anti-kb                   | anti kb         | true",
            "an alt account            | 'alt '          | true",
            "alts                      | 'alt '          | false",
            // an entry that starts with a space or punctuation brings its own word start
            "an alt account            | ' alt'          | true",
            "halt                      | ' alt'          | false",
            "hacking/ blacklisted mods | / blacklisted   | true",
            "ban#30d                   | '#30d'          | true",
    })
    void anEntryMatchesWhereItStartsAWord(String text, String entry, boolean expected) {
        assertEquals(expected, BanClassifier.matches(text, List.of(entry)));
    }

    /**
     * Chinese, Japanese and Thai put no space between words, so a letter of theirs does not continue a word: "使用外挂"
     * is "using cheats", "外部チート" "external cheat", "ใช้โปรแกรมโกง" "using a cheat program". Korean has spaces.
     */
    @ParameterizedTest(name = "{1} in {0} -> {2}")
    @CsvSource(delimiter = '|', value = {
            "使用外挂          | 外挂     | true",
            "使用killaura      | killaura | true",
            "外部チート        | チート   | true",
            "ｸﾗｲｱﾝﾄﾁｰﾄ         | ﾁｰﾄ      | true",
            "ใช้โปรแกรมโกง      | โกง      | true",
            "hack外挂          | 外挂     | true",
            "에임봇            | 봇       | false",
            "breach 外挂       | reach    | false",
    })
    void wordsOfScriptsWithoutSpacesStartAnywhere(String text, String entry, boolean expected) {
        assertEquals(expected, BanClassifier.matches(text, List.of(entry)));
    }

    /** Reasons keep the colour codes staff type (LiteBans shows them coloured): a code before a word starts it. */
    @ParameterizedTest(name = "{0} -> {1}")
    @CsvSource(delimiter = '|', value = {
            "&cHacking                              | CHEAT",
            "&4&lBlacklisted &cModifications        | CHEAT",
            "§cKillAura                             | CHEAT",
            "&#ff5555Cheating                       | CHEAT",
            "§x§f§f§5§5§5§5Cheating                 | CHEAT",
            "Hacking &7(&cX-Ray&7)                  | NONE",
            "&cHacking &8- &7alt of a banned player | NONE",
            "&cBreach of trade rules                | NONE",
            "&cDescription spam                     | NONE",
    })
    void colourCodesDoNotHideAWordStart(String reason, Label expected) {
        assertEquals(expected, classifier.added(ban("ban", reason, STAFF, null)));
    }

    @Test
    void aQuotedEntryWithALeadingSpaceStillMatchesAfterAWord() {
        BanClassifier spaced = new BanClassifier(new LiteBansSettings(true, LiteBansSettings.DEFAULT_CHEAT_REASONS,
                List.of(" alt "), LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS, false, 30));
        assertEquals(Label.NONE, spaced.added(ban("ban", "Hacking on an alt account", STAFF, null)));
        assertEquals(Label.CHEAT, spaced.added(ban("ban", "Hacking (halted)", STAFF, null)));
    }

    @Test
    void nothingMatchesWithoutTextOrEntries() {
        assertFalse(BanClassifier.matches(null, List.of("hack")));
        assertFalse(BanClassifier.matches("", List.of("hack")));
        assertFalse(BanClassifier.matches("hacking", List.of()));
        assertTrue(BanClassifier.matches("hacking", List.of("cheat", "hack")), "any entry");
    }

    @Test
    void matchingDoesNotDependOnTheDefaultLocale() {
        Locale before = Locale.getDefault();
        Locale.setDefault(Locale.forLanguageTag("tr"));
        try {
            assertTrue(BanClassifier.matches("HITBOX", List.of("hitbox")), "no dotless i");
            assertEquals(List.of("hitbox"),
                    new LiteBansSettings(true, List.of("HITBOX"), null, null, false, 30).cheatReasons());
        } finally {
            Locale.setDefault(before);
        }
    }

    @Test
    void theDefaultListsAreDistinctLowercaseAndStartWithALetter() {
        for (List<String> list : List.of(LiteBansSettings.DEFAULT_CHEAT_REASONS,
                LiteBansSettings.DEFAULT_IGNORE_REASONS, LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS)) {
            assertEquals(list.size(), Set.copyOf(list).size(), list.toString());
            for (String entry : list) {
                assertEquals(entry.toLowerCase(Locale.ROOT), entry);
                assertTrue(Character.isLetter(entry.charAt(0)), entry);
            }
        }
    }

    @Test
    void everyDefaultCheatReasonOnItsOwnIsACheatBan() {
        for (String entry : LiteBansSettings.DEFAULT_CHEAT_REASONS) {
            assertEquals(Label.CHEAT, classifier.added(ban("ban", entry.toUpperCase(Locale.ROOT), STAFF, null)), entry);
            assertEquals(Label.CHEAT, classifier.added(ban("ban", "Banned for " + entry, STAFF, null)), entry);
        }
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
        assertEquals(Label.CHEAT, countingConsole.added(ban("ban", "cheating (automatic detection)", "CONSOLE", null)));
        assertEquals(Label.NONE, countingConsole.added(ban("ban", "bot account (spam joins)", "CONSOLE", null)));
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
