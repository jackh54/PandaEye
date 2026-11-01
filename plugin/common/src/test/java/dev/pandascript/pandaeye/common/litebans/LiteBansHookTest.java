package dev.pandascript.pandaeye.common.litebans;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel;
import litebans.api.Database;
import litebans.api.Entry;
import litebans.api.Events;
import litebans.api.TestLiteBans;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.sql.PreparedStatement;
import java.util.ArrayList;
import java.util.List;

import static dev.pandascript.pandaeye.common.litebans.BanClassifierTest.PLAYER;
import static dev.pandascript.pandaeye.common.litebans.BanClassifierTest.STAFF;
import static dev.pandascript.pandaeye.common.litebans.FakeHistory.row;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The hook against the LiteBans API stub: registration, forwarding, the database and errors. */
class LiteBansHookTest {

    private static final long START_MS = 1_792_000_000_000L;

    private final List<Event> sent = new ArrayList<>();
    private final List<Runnable> scheduled = new ArrayList<>();
    private final TestLog log = new TestLog();

    @AfterEach
    void uninstall() {
        TestLiteBans.uninstall();
    }

    /** A LiteBans entry; {@code reason} "throw" makes {@link #getReason()} throw. */
    static final class FakeEntry extends Entry {

        final long id;
        final String type;
        final String reason;
        final String executor;
        final String removalReason;

        FakeEntry(long id, String type, String reason, String executor, String removalReason) {
            this.id = id;
            this.type = type;
            this.reason = reason;
            this.executor = executor;
            this.removalReason = removalReason;
        }

        @Override
        public long getId() {
            return id;
        }

        @Override
        public String getType() {
            return type;
        }

        @Override
        public String getUuid() {
            return PLAYER;
        }

        @Override
        public String getReason() {
            if (reason.equals("throw")) {
                throw new IllegalStateException("entry not loaded");
            }
            return reason;
        }

        @Override
        public String getExecutorUUID() {
            return executor;
        }

        @Override
        public String getExecutorName() {
            return "Moderator_Bob";
        }

        @Override
        public String getRemovedByUUID() {
            return removalReason == null ? null : STAFF;
        }

        @Override
        public String getRemovedByName() {
            return removalReason == null ? null : "Moderator_Bob";
        }

        @Override
        public String getRemovalReason() {
            return removalReason;
        }

        @Override
        public long getDateStart() {
            return START_MS;
        }

        @Override
        public long getDateEnd() {
            return -1;
        }

        @Override
        public String getServerScope() {
            return "*";
        }

        @Override
        public String getServerOrigin() {
            return "survival";
        }

        @Override
        public boolean isSilent() {
            return false;
        }

        @Override
        public boolean isIpban() {
            return false;
        }

        @Override
        public boolean isActive() {
            return removalReason == null;
        }
    }

    private LiteBansLabels labels() {
        return LiteBansLabels.create(LiteBansSettings.DEFAULT, getClass().getClassLoader(), sent::add,
                new EventClock(START_MS * 1_000, 0, () -> 0), (task, delay) -> scheduled.add(task), log);
    }

    @Test
    void registersWithLiteBansForwardsBansAndUnregisters() {
        TestLiteBans.RecordingEvents events = new TestLiteBans.RecordingEvents();
        TestLiteBans.install(events, null);
        LiteBansLabels labels = labels();
        labels.start();
        assertEquals(1, events.listeners.size());
        Events.Listener listener = events.listeners.get(0);
        assertTrue(listener instanceof LiteBansHook);
        assertTrue(log.contains("LiteBans labels on"), log.dump());

        listener.entryAdded(new FakeEntry(10, "ban", "cheating", STAFF, null));
        listener.entryAdded(new FakeEntry(11, "mute", "cheating", STAFF, null));
        listener.entryAdded(new FakeEntry(12, "kick", "cheating", STAFF, null));
        listener.entryRemoved(new FakeEntry(10, "ban", "cheating", STAFF, "false ban"));
        listener.broadcastSent("Steve was banned", "banned");
        assertEquals(2, sent.size());
        ModerationLabel added = sent.get(0).getLabel();
        assertEquals(10, added.getEntryId());
        assertEquals(ModerationLabel.Kind.KIND_BAN_ADDED, added.getKind());
        assertEquals(ModerationLabel.Classification.CLASSIFICATION_CHEAT, added.getClassification());
        assertEquals(START_MS, added.getBanStartUnixMs());
        assertTrue(added.getPermanent());
        ModerationLabel removed = sent.get(1).getLabel();
        assertEquals(ModerationLabel.Kind.KIND_BAN_REMOVED, removed.getKind());
        assertEquals(ModerationLabel.Classification.CLASSIFICATION_LEGIT, removed.getClassification());
        assertTrue(removed.getRemovedUnixMs() > START_MS, "the unban happened now");

        labels.stop();
        assertTrue(events.listeners.isEmpty());
        listener.entryAdded(new FakeEntry(13, "ban", "cheating", STAFF, null));
        assertEquals(2, sent.size(), "nothing after stop");
    }

    @Test
    void aBrokenEntryNeverThrowsIntoLiteBans() {
        TestLiteBans.RecordingEvents events = new TestLiteBans.RecordingEvents();
        TestLiteBans.install(events, null);
        labels().start();
        Events.Listener listener = events.listeners.get(0);
        assertDoesNotThrow(() -> listener.entryAdded(new FakeEntry(20, "ban", "throw", STAFF, null)));
        assertDoesNotThrow(() -> listener.entryRemoved(new FakeEntry(20, "ban", "throw", STAFF, "false ban")));
        assertDoesNotThrow(() -> listener.entryAdded(null));
        assertTrue(sent.isEmpty());
        assertTrue(log.contains("could not label a LiteBans entry"), log.dump());
    }

    @Test
    void waitsOnceForALiteBansThatIsNotUpYet() {
        LiteBansLabels labels = labels();
        labels.start();
        assertEquals("waiting for LiteBans", labels.status());
        assertEquals(1, scheduled.size());
        TestLiteBans.RecordingEvents events = new TestLiteBans.RecordingEvents();
        TestLiteBans.install(events, null);
        scheduled.get(0).run();
        assertEquals(1, events.listeners.size());
        assertTrue(labels.status().startsWith("on"), labels.status());
    }

    @Test
    void theBackfillQueriesLiteBansDatabase() {
        FakeHistory history = new FakeHistory(List.of(row(7, PLAYER, "auto clicker", STAFF, null, null, START_MS, -1)));
        List<String> statements = new ArrayList<>();
        TestLiteBans.install(new TestLiteBans.RecordingEvents(), new Database() {
            @Override
            public PreparedStatement prepareStatement(String sql) {
                statements.add(sql);
                return history.statement();
            }
        });
        LiteBansLabels labels = labels();
        labels.start();
        assertTrue(statements.isEmpty(), "the backfill waits for the async scheduler");
        scheduled.get(0).run();
        assertEquals(List.of(LiteBansLabels.HISTORY_SQL), statements);
        assertEquals(1, sent.size());
        assertEquals(7, sent.get(0).getLabel().getEntryId());
    }

    @Test
    void aMissingDatabaseFailsTheBackfillOnly() {
        TestLiteBans.install(new TestLiteBans.RecordingEvents(), null);
        LiteBansLabels labels = labels();
        labels.start();
        scheduled.get(0).run();
        assertTrue(labels.status().contains("not connected"), labels.status());
        assertTrue(log.contains("LiteBans backfill failed"), log.dump());
    }
}
