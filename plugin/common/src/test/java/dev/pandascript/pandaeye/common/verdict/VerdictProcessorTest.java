package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Action;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The verdict decision table of ARCHITECTURE 4.7 / PROTOCOL 7.2-7.4. */
class VerdictProcessorTest {

    private static final UUID STEVE = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");
    private static final UUID ALEX = UUID.fromString("61699b2e-d327-4a01-9f1e-0ea8c3f06bc6");

    @TempDir
    Path dir;

    private FakeVerdictPlatform platform;
    private final List<String> acks = new ArrayList<>();
    private final TestLog log = new TestLog();
    private VerdictProcessor processor;
    private int ids;

    @BeforeEach
    void setUp() {
        platform = new FakeVerdictPlatform();
        platform.online.put(STEVE, "Steve");
        processor = open();
    }

    private VerdictProcessor open() {
        ExecutedStore store = ExecutedStore.load(dir.resolve(ExecutedStore.FILE_NAME), log, platform.now);
        return new VerdictProcessor(platform, store, (id, executed, note) -> acks.add(id + "|" + executed + "|" + note), log);
    }

    private Verdict.Builder verdict(Action action) {
        return Verdict.newBuilder()
                .setVerdictId("00000000-0000-4000-8000-" + String.format("%012d", ++ids))
                .setPlayer(Uuids.toByteString(STEVE))
                .setPlayerName("Steve")
                .setAction(action)
                .setReason("Combat heuristics (killaura.hitbox x7, aim.snap x3)")
                .addChecks("killaura.hitbox")
                .addChecks("aim.snap")
                .setScore(12.5)
                .setConfidence(0.97)
                .setCreatedUnixMs(platform.now - 1_000)
                .setAppealCode("AB12-CD34");
    }

    private VerdictProcessor.Outcome handle(Verdict.Builder verdict) {
        return processor.handle(verdict.build());
    }

    private static void assertOutcome(boolean executed, String note, VerdictProcessor.Outcome outcome) {
        assertEquals(note, outcome.note());
        assertEquals(executed, outcome.executed(), note);
    }

    // ---------------------------------------------------------------------------------------------
    // Shadow: every mode combination x every enforcing action.
    // ---------------------------------------------------------------------------------------------

    static Stream<Arguments> modes() {
        List<Arguments> rows = new ArrayList<>();
        for (Action action : List.of(Action.ACTION_ALERT, Action.ACTION_MITIGATE, Action.ACTION_UNMITIGATE,
                Action.ACTION_KICK, Action.ACTION_BAN)) {
            for (boolean live : new boolean[]{false, true}) {
                for (boolean enforcement : new boolean[]{false, true}) {
                    for (boolean shadowVerdict : new boolean[]{false, true}) {
                        rows.add(Arguments.of(action, live, enforcement, shadowVerdict));
                    }
                }
            }
        }
        return rows.stream();
    }

    @ParameterizedTest(name = "{0} live={1} enforcement={2} verdict.shadow={3}")
    @MethodSource("modes")
    void shadowGatesEveryEnforcingAction(Action action, boolean live, boolean enforcement, boolean shadowVerdict) {
        platform.live = live;
        platform.enforcement = enforcement;
        VerdictProcessor.Outcome outcome = handle(verdict(action).setShadow(shadowVerdict)
                .addMitigations(Mitigation.MITIGATION_HIT_CANCEL).setStrength(0.5));
        String expected;
        if (!live) {
            expected = VerdictNotes.SHADOW_LOCAL;
        } else if (!enforcement) {
            expected = VerdictNotes.SHADOW_ENFORCEMENT_OFF;
        } else if (shadowVerdict) {
            expected = VerdictNotes.SHADOW_VERDICT;
        } else {
            expected = switch (action) {
                case ACTION_ALERT -> VerdictNotes.ALERTED;
                case ACTION_MITIGATE -> VerdictNotes.MITIGATED;
                case ACTION_UNMITIGATE -> VerdictNotes.NOTHING_TO_UNMITIGATE;
                case ACTION_KICK -> VerdictNotes.KICKED;
                default -> VerdictNotes.BANNED;
            };
        }
        boolean enforced = live && enforcement && !shadowVerdict;
        assertOutcome(enforced, expected, outcome);
        if (!enforced) {
            assertEquals(0, platform.sideEffects(), "nothing may be executed in shadow");
        }
        assertEquals(1, acks.size());
        assertEquals(1, platform.alerts.size(), "every verdict is announced");
    }

    // ---------------------------------------------------------------------------------------------
    // UNBAN: the any-mode exception.
    // ---------------------------------------------------------------------------------------------

    @ParameterizedTest(name = "live={0} enforcement={1} verdict.shadow={2}")
    @MethodSource("unbanModes")
    void unbanOfAnOwnBanRunsInAnyMode(boolean live, boolean enforcement, boolean shadowVerdict) {
        Verdict.Builder ban = verdict(Action.ACTION_BAN);
        assertOutcome(true, VerdictNotes.BANNED, handle(ban));
        platform.live = live;
        platform.enforcement = enforcement;
        platform.online.clear();
        VerdictProcessor.Outcome outcome = handle(verdict(Action.ACTION_UNBAN).setShadow(shadowVerdict)
                .setSupersedes(ban.getVerdictId()));
        assertOutcome(true, VerdictNotes.UNBANNED, outcome);
        assertEquals(List.of("pardon Steve"), platform.unbanCommands);
        assertTrue(platform.commands.stream().noneMatch(c -> c.startsWith("pardon")));
        assertOutcome(false, VerdictNotes.NOT_EXECUTED_HERE,
                handle(verdict(Action.ACTION_UNBAN).setSupersedes(ban.getVerdictId())));
    }

    static Stream<Arguments> unbanModes() {
        List<Arguments> rows = new ArrayList<>();
        for (boolean live : new boolean[]{false, true}) {
            for (boolean enforcement : new boolean[]{false, true}) {
                for (boolean shadow : new boolean[]{false, true}) {
                    rows.add(Arguments.of(live, enforcement, shadow));
                }
            }
        }
        return rows.stream();
    }

    @Test
    void unbanOfABanNotExecutedHereDoesNothing() {
        platform.live = false;
        assertOutcome(false, VerdictNotes.NOT_EXECUTED_HERE,
                handle(verdict(Action.ACTION_UNBAN).setSupersedes("11111111-1111-4111-8111-111111111111")));
        platform.live = true;
        assertOutcome(false, VerdictNotes.NOT_EXECUTED_HERE, handle(verdict(Action.ACTION_UNBAN)));
        assertEquals(0, platform.sideEffects());
    }

    @Test
    void aShadowBanIsNotABanExecutedHere() {
        platform.enforcement = false;
        Verdict.Builder ban = verdict(Action.ACTION_BAN);
        assertOutcome(false, VerdictNotes.SHADOW_ENFORCEMENT_OFF, handle(ban));
        platform.enforcement = true;
        assertOutcome(false, VerdictNotes.NOT_EXECUTED_HERE,
                handle(verdict(Action.ACTION_UNBAN).setSupersedes(ban.getVerdictId())));
        assertTrue(platform.unbanCommands.isEmpty());
    }

    @Test
    void aBanOlderThanThePlayersLastUnbanIsNotExecuted() {
        platform.live = false;
        handle(verdict(Action.ACTION_UNBAN).setCreatedUnixMs(platform.now - 10_000).setSupersedes("x"));
        platform.live = true;
        assertOutcome(false, VerdictNotes.BAN_BEFORE_UNBAN,
                handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now - 20_000)));
        assertOutcome(true, VerdictNotes.BANNED, handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now - 5_000)));
        assertEquals(1, platform.commands.size());
    }

    // ---------------------------------------------------------------------------------------------
    // Staleness and players.
    // ---------------------------------------------------------------------------------------------

    @Test
    void staleKicksAndBansAreNotExecuted() {
        assertOutcome(false, VerdictNotes.STALE_KICK,
                handle(verdict(Action.ACTION_KICK).setCreatedUnixMs(platform.now - 5 * 60_000 - 1)));
        assertOutcome(true, VerdictNotes.KICKED,
                handle(verdict(Action.ACTION_KICK).setCreatedUnixMs(platform.now - 5 * 60_000)));
        assertOutcome(false, VerdictNotes.STALE_BAN,
                handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now - 24 * 3_600_000L - 1)));
        assertOutcome(false, VerdictNotes.STALE_BAN, handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(0)));
        assertOutcome(true, VerdictNotes.BANNED,
                handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now - 24 * 3_600_000L)));
        assertOutcome(true, VerdictNotes.BANNED,
                handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now + 60_000)), "clock skew: future is fresh");
    }

    private static void assertOutcome(boolean executed, String note, VerdictProcessor.Outcome outcome, String why) {
        assertEquals(note, outcome.note(), why);
        assertEquals(executed, outcome.executed(), why);
    }

    @Test
    void kickNeedsAnOnlinePlayerAndUsesPlayerKickWithoutACommand() {
        assertOutcome(true, VerdictNotes.KICKED, handle(verdict(Action.ACTION_KICK)));
        assertEquals(List.of("Steve: Unfair advantage detected (Combat heuristics (killaura.hitbox x7, aim.snap x3))"),
                platform.kicks);
        assertTrue(platform.commands.isEmpty());
        assertOutcome(false, VerdictNotes.PLAYER_OFFLINE,
                handle(verdict(Action.ACTION_KICK).setPlayer(Uuids.toByteString(ALEX))));
    }

    @Test
    void kickCommandIsRunWhenConfigured() {
        platform.punishments = new PunishmentSettings("kick %player% %reason%", "", "", "", "", 3, 5);
        assertOutcome(true, VerdictNotes.KICKED, handle(verdict(Action.ACTION_KICK)));
        assertEquals(List.of("kick Steve Combat heuristics (killaura.hitbox x7, aim.snap x3)"), platform.commands);
        assertTrue(platform.kicks.isEmpty());
    }

    @Test
    void banWorksForOfflinePlayersFromTheOfflineCache() {
        platform.cache.put(ALEX, "Alex_01");
        assertOutcome(true, VerdictNotes.BANNED, handle(verdict(Action.ACTION_BAN).setPlayer(Uuids.toByteString(ALEX))));
        assertEquals(List.of("ban Alex_01 Unfair advantage. Appeal at https://appeal.example/a with code AB12-CD34"),
                platform.commands);
    }

    @Test
    void unresolvableOrInvalidNamesAreUnknownPlayers() {
        UUID nobody = UUID.randomUUID();
        assertOutcome(false, VerdictNotes.UNKNOWN_PLAYER,
                handle(verdict(Action.ACTION_BAN).setPlayer(Uuids.toByteString(nobody)).setPlayerName("Nobody")));
        platform.cache.put(nobody, "evil name; op me");
        assertOutcome(false, VerdictNotes.UNKNOWN_PLAYER,
                handle(verdict(Action.ACTION_BAN).setPlayer(Uuids.toByteString(nobody))));
        platform.online.put(STEVE, "Stéve");
        assertOutcome(false, VerdictNotes.UNKNOWN_PLAYER, handle(verdict(Action.ACTION_BAN)));
        assertOutcome(false, VerdictNotes.UNKNOWN_PLAYER, handle(verdict(Action.ACTION_KICK)));
        assertOutcome(false, VerdictNotes.UNKNOWN_PLAYER,
                handle(verdict(Action.ACTION_KICK).setPlayer(com.google.protobuf.ByteString.copyFrom(new byte[3]))));
        assertEquals(0, platform.sideEffects());
    }

    @Test
    void exemptPlayersAreNotPunished() {
        platform.exempt.add(STEVE);
        assertOutcome(false, VerdictNotes.EXEMPT, handle(verdict(Action.ACTION_KICK)));
        assertOutcome(false, VerdictNotes.EXEMPT, handle(verdict(Action.ACTION_BAN)));
        assertOutcome(false, VerdictNotes.EXEMPT, handle(verdict(Action.ACTION_MITIGATE)
                .addMitigations(Mitigation.MITIGATION_SETBACK)));
        assertEquals(0, platform.sideEffects());
    }

    @Test
    void missingCommandsDisableTheAction() {
        platform.punishments = new PunishmentSettings("", "bye", "", "", "", 3, 5);
        assertOutcome(false, VerdictNotes.NO_BAN_COMMAND, handle(verdict(Action.ACTION_BAN)));
    }

    // ---------------------------------------------------------------------------------------------
    // Mitigations.
    // ---------------------------------------------------------------------------------------------

    @Test
    void mitigateSetsTheSetAndClampedStrengthAndUnmitigateClearsItInAnyMode() {
        assertOutcome(true, VerdictNotes.MITIGATED, handle(verdict(Action.ACTION_MITIGATE)
                .addMitigations(Mitigation.MITIGATION_HIT_CANCEL).addMitigations(Mitigation.MITIGATION_SETBACK)
                .addMitigations(Mitigation.MITIGATION_UNSPECIFIED).setStrength(7)));
        assertEquals(Set.of(Mitigation.MITIGATION_HIT_CANCEL, Mitigation.MITIGATION_SETBACK), platform.mitigations.get(STEVE));
        assertEquals(1.0, platform.strengths.get(STEVE));
        platform.live = false;
        assertOutcome(true, VerdictNotes.UNMITIGATED, handle(verdict(Action.ACTION_UNMITIGATE)));
        assertTrue(platform.mitigations.isEmpty());
        assertOutcome(false, VerdictNotes.SHADOW_LOCAL, handle(verdict(Action.ACTION_UNMITIGATE)));
    }

    @Test
    void mitigateGates() {
        Verdict.Builder m = verdict(Action.ACTION_MITIGATE).addMitigations(Mitigation.MITIGATION_PLACE_CANCEL);
        platform.mitigationsEnabled = false;
        assertOutcome(false, VerdictNotes.MITIGATIONS_DISABLED, handle(m.clone().setVerdictId("m1")));
        platform.mitigationsEnabled = true;
        platform.mitigationsSupported = false;
        assertOutcome(false, VerdictNotes.MITIGATIONS_UNSUPPORTED, handle(m.clone().setVerdictId("m2")));
        platform.mitigationsSupported = true;
        assertOutcome(false, VerdictNotes.NO_MITIGATIONS, handle(verdict(Action.ACTION_MITIGATE)));
        assertOutcome(false, VerdictNotes.PLAYER_OFFLINE,
                handle(m.clone().setVerdictId("m3").setPlayer(Uuids.toByteString(ALEX))));
        assertOutcome(false, VerdictNotes.PLAYER_OFFLINE, handle(verdict(Action.ACTION_UNMITIGATE)
                .setPlayer(Uuids.toByteString(ALEX))));
        assertEquals(0, platform.sideEffects());
    }

    // ---------------------------------------------------------------------------------------------
    // Circuit breaker.
    // ---------------------------------------------------------------------------------------------

    @Test
    void theFourthBanInAnHourOpensTheBreakerUntilReset() {
        for (int i = 0; i < 3; i++) {
            assertOutcome(true, VerdictNotes.BANNED, handle(verdict(Action.ACTION_BAN)));
        }
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN, handle(verdict(Action.ACTION_BAN)));
        assertEquals(1, platform.urgent.size());
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN, handle(verdict(Action.ACTION_KICK)));
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN, handle(verdict(Action.ACTION_MITIGATE)
                .addMitigations(Mitigation.MITIGATION_HIT_CANCEL)));
        assertOutcome(true, VerdictNotes.ALERTED, handle(verdict(Action.ACTION_ALERT)));
        platform.now += 2 * 3_600_000L;
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN,
                handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now)), "the breaker latches");
        assertEquals(1, platform.urgent.size(), "one urgent alert per trip");
        assertEquals(3, platform.commands.size());
        processor.resetBreaker("test");
        assertOutcome(true, VerdictNotes.BANNED, handle(verdict(Action.ACTION_BAN).setCreatedUnixMs(platform.now)));
    }

    @Test
    void theSixthKickInTenMinutesOpensTheBreaker() {
        for (int i = 0; i < 5; i++) {
            assertOutcome(true, VerdictNotes.KICKED, handle(verdict(Action.ACTION_KICK)));
        }
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN, handle(verdict(Action.ACTION_KICK)));
        assertTrue(processor.breakerStatus().open());
    }

    @Test
    void unbanStillRunsWhileTheBreakerIsOpen() {
        Verdict.Builder ban = verdict(Action.ACTION_BAN);
        handle(ban);
        handle(verdict(Action.ACTION_BAN));
        handle(verdict(Action.ACTION_BAN));
        handle(verdict(Action.ACTION_BAN));
        assertTrue(processor.breakerStatus().open());
        assertOutcome(true, VerdictNotes.UNBANNED, handle(verdict(Action.ACTION_UNBAN).setSupersedes(ban.getVerdictId())));
    }

    @Test
    void theBreakerAndItsWindowsSurviveARestart() {
        for (int i = 0; i < 3; i++) {
            handle(verdict(Action.ACTION_BAN));
        }
        processor = open();
        assertEquals(3, processor.breakerStatus().bansLastHour());
        assertOutcome(false, VerdictNotes.CIRCUIT_OPEN, handle(verdict(Action.ACTION_BAN)));
        processor = open();
        assertTrue(processor.breakerStatus().open(), "an open breaker stays open across restarts");
        processor.resetBreaker("test");
        processor = open();
        assertFalse(processor.breakerStatus().open());
    }

    // ---------------------------------------------------------------------------------------------
    // De-duplication, recording.
    // ---------------------------------------------------------------------------------------------

    @Test
    void aRedeliveredVerdictIsAnsweredAgainButExecutedOnce() {
        Verdict ban = verdict(Action.ACTION_BAN).build();
        processor.handle(ban);
        processor.handle(ban);
        assertEquals(1, platform.commands.size());
        assertEquals(2, acks.size());
        assertEquals(acks.get(0), acks.get(1));
        assertEquals(1, platform.alerts.size(), "a duplicate is not announced again");
    }

    @Test
    void deduplicationSurvivesARestart() {
        Verdict ban = verdict(Action.ACTION_BAN).build();
        Verdict kick = verdict(Action.ACTION_KICK).build();
        Verdict mitigate = verdict(Action.ACTION_MITIGATE).addMitigations(Mitigation.MITIGATION_SETBACK).build();
        processor.handle(ban);
        processor.handle(kick);
        processor.handle(mitigate);
        assertEquals(3, platform.sideEffects());
        processor = open();
        platform.mitigations.clear();
        assertOutcome(true, VerdictNotes.ALREADY_EXECUTED, processor.handle(ban));
        assertOutcome(true, VerdictNotes.ALREADY_EXECUTED, processor.handle(kick));
        assertOutcome(true, VerdictNotes.ALREADY_EXECUTED, processor.handle(mitigate));
        assertEquals(1, platform.commands.size());
        assertEquals(1, platform.kicks.size());
        assertTrue(platform.mitigations.isEmpty());
        // The ban is still known as executed here, so its UNBAN runs after the restart.
        assertOutcome(true, VerdictNotes.UNBANNED, handle(verdict(Action.ACTION_UNBAN).setSupersedes(ban.getVerdictId())));
    }

    @Test
    void anExecutionThatCannotBeRecordedIsNotPerformed() throws IOException {
        Path notADirectory = dir.resolve("file");
        Files.writeString(notADirectory, "x");
        ExecutedStore unwritable = ExecutedStore.load(notADirectory.resolve(ExecutedStore.FILE_NAME), log, platform.now);
        processor = new VerdictProcessor(platform, unwritable, (id, executed, note) -> acks.add(note), log);
        assertOutcome(false, VerdictNotes.NOT_RECORDED, handle(verdict(Action.ACTION_BAN)));
        assertOutcome(false, VerdictNotes.NOT_RECORDED, handle(verdict(Action.ACTION_KICK)));
        assertEquals(0, platform.sideEffects());
        assertEquals(0, processor.breakerStatus().bansLastHour(), "an unrecorded ban does not count");
        assertEquals(0, processor.breakerStatus().kicksLast10Min());
        assertEquals(0, unwritable.executedCount());
    }

    @Test
    void anActionRefusedAtExecutionIsRolledBack() {
        platform.refuseSideEffects = true;
        Verdict ban = verdict(Action.ACTION_BAN).build();
        assertOutcome(false, VerdictNotes.ENFORCEMENT_CHANGED, processor.handle(ban));
        assertFalse(processor.store().executed(ban.getVerdictId()));
        assertEquals(0, processor.store().banCount());
        assertEquals(0, processor.breakerStatus().bansLastHour());
        assertOutcome(false, VerdictNotes.ENFORCEMENT_CHANGED, handle(verdict(Action.ACTION_KICK)));
    }

    @Test
    void unknownActionsAndEmptyIdsAreNotExecuted() {
        assertOutcome(false, VerdictNotes.UNSUPPORTED_ACTION, handle(verdict(Action.ACTION_UNSPECIFIED)));
        assertOutcome(false, VerdictNotes.INVALID, handle(verdict(Action.ACTION_BAN).setVerdictId("")));
        assertEquals(0, platform.sideEffects());
    }

    @Test
    void announcementsAreSanitised() {
        handle(verdict(Action.ACTION_ALERT).setReason("x\n[Server] fake line").clearChecks()
                .addChecks("evil§c\ncheck").setPlayerName("§cFake\nName"));
        String text = platform.alerts.get(0);
        assertFalse(text.contains("\n"));
        assertFalse(text.contains("§"));
        assertTrue(text.contains("for Steve"), text);
    }
}
