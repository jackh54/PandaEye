package dev.pandascript.pandaeye.common.litebans;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel.Classification;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel.Executor;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel.Kind;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.io.InputStream;
import java.lang.reflect.Method;
import java.lang.reflect.Proxy;
import java.nio.charset.StandardCharsets;
import java.sql.PreparedStatement;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Consumer;

import static dev.pandascript.pandaeye.common.litebans.BanClassifierTest.PLAYER;
import static dev.pandascript.pandaeye.common.litebans.BanClassifierTest.STAFF;
import static dev.pandascript.pandaeye.common.litebans.FakeHistory.row;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotSame;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Labels from LiteBans bans (ARCHITECTURE 4.12): events, registration, backfill, and nothing without LiteBans. */
class LiteBansLabelsTest {

    private static final long NOW_MS = 1_793_000_000_000L;
    private static final long START_MS = NOW_MS - TimeUnit.DAYS.toMillis(2);
    private static final long EVENT_US = 1_793_000_000_123_456L;
    private static final String BEDROCK = "00000000-0000-0000-0009-01f2a3b4c5d6";

    private final List<Event> sent = new ArrayList<>();
    private final TestLog log = new TestLog();
    private final List<Scheduled> scheduled = new ArrayList<>();
    private final FakeApi api = new FakeApi();
    private final EventClock clock = new EventClock(EVENT_US, 0, () -> 0);

    record Scheduled(Runnable task, long delayMillis) {
    }

    /** LiteBans as LiteBansLabels sees it. */
    static final class FakeApi implements LiteBansLabels.Api {

        boolean ready = true;
        LinkageError registerError;
        LiteBansLabels registered;
        int registrations;
        int unregistrations;
        final List<String> statements = new ArrayList<>();
        FakeHistory history;

        @Override
        public boolean register(LiteBansLabels labels) {
            registrations++;
            if (registerError != null) {
                throw registerError;
            }
            if (!ready) {
                return false;
            }
            registered = labels;
            return true;
        }

        @Override
        public void unregister() {
            unregistrations++;
            registered = null;
        }

        @Override
        public PreparedStatement prepare(String sql) throws SQLException {
            statements.add(sql);
            if (history == null) {
                throw new SQLException("Communications link failure");
            }
            return history.statement();
        }
    }

    private LiteBansLabels labels(LiteBansSettings settings) {
        return new LiteBansLabels(settings, api, sent::add, clock, () -> NOW_MS,
                (task, delay) -> scheduled.add(new Scheduled(task, delay)), log, "starting");
    }

    private LiteBansLabels started() {
        LiteBansLabels labels = labels(LiteBansSettings.DEFAULT);
        labels.start();
        return labels;
    }

    private static BanEntry ban(long id, String player, String reason, String executor, String removalReason,
                                long endMs) {
        return new BanEntry(id, "ban", player, reason, executor, removalReason, START_MS, endMs);
    }

    private static Event expected(long id, String player, Kind kind, Classification classification, long removedMs,
                                  boolean permanent, Executor executor) {
        return Event.newBuilder().setPlayer(Uuids.toByteString(UUID.fromString(player))).setTimeUs(EVENT_US)
                .setLabel(ModerationLabel.newBuilder().setSource("litebans").setEntryId(id).setKind(kind)
                        .setClassification(classification).setBanStartUnixMs(START_MS).setRemovedUnixMs(removedMs)
                        .setPermanent(permanent).setExecutor(executor))
                .build();
    }

    // ---------------------------------------------------------------------------------------------
    // Live LiteBans events
    // ---------------------------------------------------------------------------------------------

    @Test
    void aCheatBanAndItsWrongfulUnbanBecomeOneEventEach() {
        LiteBansLabels labels = started();
        assertSame(labels, api.registered);
        labels.added(ban(17, PLAYER, "hacking / blacklisted modifications", STAFF, null, -1));
        labels.removed(ban(17, PLAYER, "hacking / blacklisted modifications", STAFF, "appeal accepted (ticket #812)", -1));
        assertEquals(List.of(
                expected(17, PLAYER, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                        Executor.EXECUTOR_STAFF),
                expected(17, PLAYER, Kind.KIND_BAN_REMOVED, Classification.CLASSIFICATION_LEGIT, NOW_MS, true,
                        Executor.EXECUTOR_STAFF)), sent);
        assertTrue(labels.status().startsWith("on, 1 cheat and 1 legit labels sent"), labels.status());
    }

    @Test
    void theEventCarriesNoReasonNameOrStaffIdentity() {
        Event event = LiteBansLabels.event(ban(5, PLAYER, "macros and scripts", STAFF, "false ban (Moderator_Bob)",
                START_MS + 86_400_000L), UUID.fromString(PLAYER), Kind.KIND_BAN_REMOVED, BanClassifier.Label.LEGIT,
                NOW_MS, EVENT_US);
        String bytes = new String(event.toByteArray(), StandardCharsets.ISO_8859_1);
        for (String secret : List.of("macro", "false ban", "Moderator_Bob", STAFF)) {
            assertFalse(bytes.contains(secret), secret);
        }
        assertFalse(bytes.contains(new String(Uuids.toBytes(UUID.fromString(STAFF)), StandardCharsets.ISO_8859_1)));
        assertEquals("litebans", event.getLabel().getSource());
        assertFalse(event.getLabel().getPermanent(), "a temporary ban");
        assertEquals(Event.KindCase.LABEL, event.getKindCase());
        assertEquals(0, event.getTick());
    }

    @Test
    void consoleBansAreLabelledOnlyWithCountConsoleAndCarryTheExecutorKind() {
        LiteBansLabels labels = started();
        labels.added(ban(30, PLAYER, "cheating (automatic detection)", "CONSOLE", null, -1));
        assertTrue(sent.isEmpty(), "console bans are off by default");
        labels.removed(ban(30, PLAYER, "cheating (automatic detection)", "CONSOLE", "unban wave 2026-10-27", -1));
        assertTrue(sent.isEmpty(), "an unban wave is no evidence");
        labels.removed(ban(31, PLAYER, "cheating (automatic detection)", "CONSOLE", "false ban", -1));
        assertEquals(List.of(expected(31, PLAYER, Kind.KIND_BAN_REMOVED, Classification.CLASSIFICATION_LEGIT, NOW_MS,
                true, Executor.EXECUTOR_CONSOLE)), sent);

        sent.clear();
        LiteBansLabels counting = labels(new LiteBansSettings(true, LiteBansSettings.DEFAULT_CHEAT_REASONS,
                LiteBansSettings.DEFAULT_IGNORE_REASONS, LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS, true, 30));
        counting.start();
        counting.added(ban(32, PLAYER, "cheating (automatic detection)", "CONSOLE", null, -1));
        assertEquals(List.of(expected(32, PLAYER, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                Executor.EXECUTOR_CONSOLE)), sent);
    }

    @Test
    void entriesThatAreNotLabelsAreNotSent() {
        LiteBansLabels labels = started();
        labels.added(ban(40, PLAYER, "bot account (spam joins)", STAFF, null, -1));
        labels.added(ban(41, PLAYER, "esp/x-ray", STAFF, null, -1));
        labels.added(new BanEntry(42, "mute", PLAYER, "cheating", STAFF, null, START_MS, -1));
        labels.removed(ban(43, PLAYER, "cheating", STAFF, "ban has been lifted.", -1));
        labels.added(ban(44, null, "cheating", STAFF, null, -1));
        labels.added(ban(45, "#offline#", "cheating", STAFF, null, -1));
        labels.added(ban(0, PLAYER, "cheating", STAFF, null, -1));
        assertTrue(sent.isEmpty(), sent.toString());
        labels.added(ban(46, BEDROCK, "auto totem", STAFF, null, 0));
        assertEquals(List.of(expected(46, BEDROCK, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                Executor.EXECUTOR_STAFF)), sent, "a Bedrock player's UUID as given");
    }

    @Test
    void stopUnregistersOnceAndLateCallbacksAreIgnored() {
        LiteBansLabels labels = started();
        labels.stop();
        labels.stop();
        assertEquals(1, api.unregistrations);
        assertNull(api.registered);
        labels.added(ban(50, PLAYER, "cheating", STAFF, null, -1));
        assertTrue(sent.isEmpty());
        scheduled.get(0).task().run();
        assertTrue(api.statements.isEmpty(), "nothing queried after stop");
    }

    // ---------------------------------------------------------------------------------------------
    // Registration
    // ---------------------------------------------------------------------------------------------

    @Test
    void registrationRetriesOnceWhenLiteBansIsNotUpYet() {
        api.ready = false;
        api.history = new FakeHistory(List.of());
        LiteBansLabels labels = started();
        assertEquals("waiting for LiteBans", labels.status());
        assertEquals(1, scheduled.size());
        assertEquals(LiteBansLabels.RETRY_DELAY_MS, scheduled.get(0).delayMillis());
        api.ready = true;
        scheduled.get(0).task().run();
        assertSame(labels, api.registered);
        assertEquals(List.of(LiteBansLabels.HISTORY_SQL), api.statements, "the retry runs the backfill itself");
        assertEquals(1, scheduled.size());
        assertTrue(labels.status().startsWith("on"), labels.status());
    }

    @Test
    void liteBansThatNeverComesUpTurnsLabelsOffWithOneWarning() {
        api.ready = false;
        LiteBansLabels labels = started();
        scheduled.get(0).task().run();
        assertEquals("off (LiteBans API not ready)", labels.status());
        assertEquals(2, api.registrations);
        assertTrue(api.statements.isEmpty());
        assertTrue(log.contains("still not ready"), log.dump());
        labels.stop();
        assertEquals(0, api.unregistrations, "never registered");
    }

    @Test
    void anIncompatibleLiteBansTurnsLabelsOff() {
        api.registerError = new IncompatibleClassChangeError("litebans.api.Events");
        LiteBansLabels labels = started();
        assertEquals("off (LiteBans API not compatible)", labels.status());
        assertTrue(scheduled.isEmpty());
        assertTrue(log.contains("could not register with LiteBans"), log.dump());
    }

    // ---------------------------------------------------------------------------------------------
    // Backfill
    // ---------------------------------------------------------------------------------------------

    @Test
    void theBackfillRunsOnTheAsyncSchedulerAndLabelsTheRecentBans() throws Exception {
        Timestamp unbanned = new Timestamp(NOW_MS - 3_600_000L);
        api.history = new FakeHistory(List.of(
                row(1, PLAYER, "cheating", STAFF, null, null, START_MS, -1),
                row(2, PLAYER, "auto totem", STAFF, "false ban", unbanned, START_MS, START_MS + 86_400_000L),
                row(3, PLAYER, "cheating (automatic detection)", "CONSOLE", "unban wave 2026-10-27", unbanned, START_MS, -1),
                row(4, PLAYER, "bot account (spam joins)", STAFF, null, null, START_MS, -1),
                row(5, PLAYER, "cheating (automatic detection)", "CONSOLE", "false ban", unbanned, START_MS, -1),
                row(6, PLAYER, "x-ray", STAFF, "appeal accepted (ticket #3)", unbanned, START_MS, -1),
                row(7, null, "cheating", STAFF, null, null, START_MS, -1),
                row(8, BEDROCK, "easy place", STAFF, null, null, START_MS, 0),
                row(9, PLAYER, "cheating", STAFF, "false ban", new SQLException("zero date"), START_MS, -1)));
        LiteBansLabels labels = started();
        assertTrue(api.statements.isEmpty(), "never on the enabling (main) thread");
        assertEquals(1, scheduled.size());
        assertEquals(0, scheduled.get(0).delayMillis());

        Thread async = new Thread(scheduled.get(0).task(), "async-scheduler");
        async.start();
        async.join(5_000);
        assertEquals(async, api.history.queryThread);

        assertEquals(List.of(LiteBansLabels.HISTORY_SQL), api.statements);
        assertTrue(LiteBansLabels.HISTORY_SQL.contains("FROM {bans} WHERE time >= ?"));
        assertEquals(NOW_MS - TimeUnit.DAYS.toMillis(30), api.history.params.get(1));
        assertEquals(LiteBansLabels.MAX_BACKFILL_ROWS, api.history.maxRows);
        assertTrue(api.history.statementClosed && api.history.resultClosed);
        assertEquals(List.of(
                expected(1, PLAYER, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                        Executor.EXECUTOR_STAFF),
                expected(2, PLAYER, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, false,
                        Executor.EXECUTOR_STAFF),
                expected(2, PLAYER, Kind.KIND_BAN_REMOVED, Classification.CLASSIFICATION_LEGIT, unbanned.getTime(), false,
                        Executor.EXECUTOR_STAFF),
                expected(5, PLAYER, Kind.KIND_BAN_REMOVED, Classification.CLASSIFICATION_LEGIT, unbanned.getTime(), true,
                        Executor.EXECUTOR_CONSOLE),
                expected(8, BEDROCK, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                        Executor.EXECUTOR_STAFF),
                expected(9, PLAYER, Kind.KIND_BAN_ADDED, Classification.CLASSIFICATION_CHEAT, 0, true,
                        Executor.EXECUTOR_STAFF),
                expected(9, PLAYER, Kind.KIND_BAN_REMOVED, Classification.CLASSIFICATION_LEGIT, 0, true,
                        Executor.EXECUTOR_STAFF)), sent);
        assertTrue(labels.status().endsWith("backfill 9 bans of the last 30 days read"), labels.status());
        assertTrue(log.contains("LiteBans backfill: 9 bans of the last 30 days read, 4 cheat and 3 legit labels sent"),
                log.dump());
    }

    @Test
    void theBackfillStopsWithThePlugin() {
        api.history = new FakeHistory(List.of(
                row(1, PLAYER, "cheating", STAFF, null, null, START_MS, -1),
                row(2, PLAYER, "cheating", STAFF, null, null, START_MS, -1)));
        LiteBansLabels labels = started();
        api.history.afterRow = row -> labels.stop();
        scheduled.get(0).task().run();
        assertEquals(1, sent.size());
        assertTrue(api.history.statementClosed);
    }

    @Test
    void aFailedBackfillIsReportedAndLiveLabelsKeepWorking() {
        LiteBansLabels labels = started();
        scheduled.get(0).task().run();
        assertTrue(labels.status().contains("backfill failed after 0 bans"), labels.status());
        assertTrue(log.contains("LiteBans backfill failed"), log.dump());
        labels.added(ban(60, PLAYER, "cheating", STAFF, null, -1));
        assertEquals(1, sent.size());
    }

    @Test
    void zeroBackfillDaysQueriesNothing() {
        LiteBansLabels labels = labels(new LiteBansSettings(true, null, null, null, false, 0));
        labels.start();
        assertTrue(scheduled.isEmpty());
        assertTrue(api.statements.isEmpty());
        assertTrue(labels.status().endsWith("backfill off (backfill-days: 0)"), labels.status());
    }

    // ---------------------------------------------------------------------------------------------
    // Off: no LiteBans, disabled, no cloud
    // ---------------------------------------------------------------------------------------------

    @Test
    void offStatesRegisterScheduleAndSendNothing() {
        Consumer<Event> sink = sent::add;
        LiteBansLabels.Async async = (task, delay) -> scheduled.add(new Scheduled(task, delay));
        LiteBansSettings disabled = new LiteBansSettings(false, null, null, null, false, 30);
        ClassLoader stub = getClass().getClassLoader();
        ClassLoader none = new WithoutLiteBans(stub);
        assertTrue(LiteBansLabels.installed(stub), "the test class path has the LiteBans stub");
        assertFalse(LiteBansLabels.installed(none));
        for (Object[] c : new Object[][]{
                {LiteBansLabels.create(disabled, stub, sink, clock, async, log), "off (labels.litebans.enabled: false)"},
                {LiteBansLabels.create(LiteBansSettings.DEFAULT, stub, null, clock, async, log), "off (cloud off)"},
                {LiteBansLabels.create(LiteBansSettings.DEFAULT, none, sink, clock, async, log),
                        "off (LiteBans not installed)"}}) {
            LiteBansLabels labels = (LiteBansLabels) c[0];
            labels.start();
            labels.stop();
            assertEquals(c[1], labels.status());
        }
        assertTrue(sent.isEmpty());
        assertTrue(scheduled.isEmpty());
        assertEquals(List.of(), log.lines());
    }

    /**
     * A server without LiteBans, as the plugin's class loader sees it: this package's classes are loaded by a loader
     * that cannot see {@code litebans.*}. Loading, linking and running the integration must not touch LiteBans (only
     * the hook needs it, and it cannot even load there).
     */
    @Test
    void withoutLiteBansNoLiteBansClassIsEverLoaded() throws Exception {
        ClassLoader loader = new WithoutLiteBans(getClass().getClassLoader());
        Class<?> labelsType = Class.forName(LiteBansLabels.class.getName(), true, loader);
        assertNotSame(LiteBansLabels.class, labelsType);
        Class<?> settingsType = Class.forName(LiteBansSettings.class.getName(), true, loader);
        Class<?> asyncType = Class.forName(LiteBansLabels.Async.class.getName(), true, loader);
        AtomicReference<Object> scheduledTask = new AtomicReference<>();
        Object async = Proxy.newProxyInstance(loader, new Class<?>[]{asyncType}, (proxy, method, args) -> {
            scheduledTask.set(args[0]);
            return null;
        });
        Method create = labelsType.getMethod("create", settingsType, ClassLoader.class, Consumer.class,
                EventClock.class, asyncType, Log.class);
        Consumer<Event> sink = sent::add;
        Object labels = create.invoke(null, settingsType.getField("DEFAULT").get(null), loader, sink, clock, async, log);
        labelsType.getMethod("start").invoke(labels);
        labelsType.getMethod("stop").invoke(labels);
        assertEquals("off (LiteBans not installed)", labelsType.getMethod("status").invoke(labels));
        assertNull(scheduledTask.get());
        assertTrue(sent.isEmpty());
        assertEquals(List.of(), log.lines());
        assertThrows(NoClassDefFoundError.class,
                () -> Class.forName(LiteBansLabels.class.getPackageName() + ".LiteBansHook", true, loader),
                "the hook needs LiteBans (so the Paper preloader skips it)");
    }

    /** Refuses {@code litebans.*} and defines this package's main classes itself (child first). */
    static final class WithoutLiteBans extends ClassLoader {

        private static final String OWN = LiteBansLabels.class.getPackageName() + ".";

        WithoutLiteBans(ClassLoader parent) {
            super(parent);
        }

        @Override
        protected Class<?> loadClass(String name, boolean resolve) throws ClassNotFoundException {
            if (name.startsWith("litebans.")) {
                throw new ClassNotFoundException(name);
            }
            if (!name.startsWith(OWN) || name.endsWith("Test") || name.contains("Test$") || name.contains("Fake")) {
                return super.loadClass(name, resolve);
            }
            synchronized (getClassLoadingLock(name)) {
                Class<?> type = findLoadedClass(name);
                if (type == null) {
                    byte[] bytes;
                    try (InputStream in = getParent().getResourceAsStream(name.replace('.', '/') + ".class")) {
                        if (in == null) {
                            throw new ClassNotFoundException(name);
                        }
                        bytes = in.readAllBytes();
                    } catch (IOException e) {
                        throw new ClassNotFoundException(name, e);
                    }
                    type = defineClass(name, bytes, 0, bytes.length);
                }
                if (resolve) {
                    resolveClass(type);
                }
                return type;
            }
        }
    }

    @Test
    void historyRowsAreReadByColumnName() {
        Map<String, Object> row = row(1, PLAYER, "cheating", STAFF, null, null, START_MS, -1);
        assertEquals(List.of("id", "uuid", "reason", "banned_by_uuid", "removed_by_reason", "removed_by_date", "time",
                "until"), List.copyOf(row.keySet()));
        for (String column : row.keySet()) {
            assertTrue(LiteBansLabels.HISTORY_SQL.contains(column), column);
        }
    }
}
