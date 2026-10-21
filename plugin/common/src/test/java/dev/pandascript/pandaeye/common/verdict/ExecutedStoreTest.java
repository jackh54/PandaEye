package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.common.TestLog;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ExecutedStoreTest {

    private static final long T = 1_700_000_000_000L;
    private static final UUID P = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");

    @TempDir
    Path dir;

    private final TestLog log = new TestLog();

    private Path file() {
        return dir.resolve(ExecutedStore.FILE_NAME);
    }

    @Test
    void roundTripsAcrossRestarts() throws IOException {
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        store.recordExecuted(new ExecutedStore.Executed("v1", "BAN", P, T), new ExecutedStore.Ban("v1", P, "Steve", T), T);
        store.recordExecuted(new ExecutedStore.Executed("v2", "KICK", P, T + 1), null, T + 1);
        store.noteUnban(P, T - 5, T);
        store.breaker(new ExecutedStore.BreakerState(true, T, "more than 3 bans \"quoted\"\n"));

        ExecutedStore again = ExecutedStore.load(file(), log, T + 2);
        assertTrue(again.executed("v1"));
        assertTrue(again.executed("v2"));
        assertEquals("Steve", again.ban("v1").name());
        assertEquals(P, again.ban("v1").player());
        assertNull(again.ban("v2"));
        assertEquals(T - 5, again.lastUnban(P));
        assertTrue(again.breaker().open());
        assertEquals("more than 3 bans \"quoted\"\n", again.breaker().reason());
        assertEquals(List.of(T + 1), again.executedSince("KICK", T));
        assertFalse(Files.exists(dir.resolve(ExecutedStore.FILE_NAME + ".tmp")), "written atomically");
        Object parsed = MiniJson.parse(Files.readString(file()));
        assertTrue(parsed instanceof Map<?, ?>);
    }

    @Test
    void executedIdsExpireAfterSevenDaysButBansAreKept() throws IOException {
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        store.recordExecuted(new ExecutedStore.Executed("v1", "BAN", P, T), new ExecutedStore.Ban("v1", P, "Steve", T), T);
        ExecutedStore later = ExecutedStore.load(file(), log, T + ExecutedStore.KEEP_MILLIS + 1);
        assertFalse(later.executed("v1"));
        assertNotNull(later.ban("v1"), "an UNBAN can come any time");
    }

    @Test
    void rollbackAndRemoveBan() throws IOException {
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        store.recordExecuted(new ExecutedStore.Executed("v1", "BAN", P, T), new ExecutedStore.Ban("v1", P, "Steve", T), T);
        store.rollback("v1");
        ExecutedStore again = ExecutedStore.load(file(), log, T);
        assertFalse(again.executed("v1"));
        assertNull(again.ban("v1"));
        again.recordExecuted(new ExecutedStore.Executed("v2", "BAN", P, T), new ExecutedStore.Ban("v2", P, "Steve", T), T);
        again.removeBan("v2");
        assertTrue(ExecutedStore.load(file(), log, T).executed("v2"));
        assertNull(ExecutedStore.load(file(), log, T).ban("v2"));
    }

    @Test
    void isBounded() throws IOException {
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        for (int i = 0; i < ExecutedStore.MAX_EXECUTED + 10; i++) {
            store.recordExecuted(new ExecutedStore.Executed("v" + i, "MITIGATE", P, T), null, T);
        }
        assertEquals(ExecutedStore.MAX_EXECUTED, store.executedCount());
        assertFalse(store.executed("v0"), "the oldest go first");
        assertTrue(store.executed("v" + (ExecutedStore.MAX_EXECUTED + 9)));
    }

    @Test
    void aCorruptFileIsMovedAsideLoudly() throws IOException {
        Files.writeString(file(), "{\"executed\": [ {\"id\": ");
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        assertEquals(0, store.executedCount());
        assertTrue(Files.exists(dir.resolve(ExecutedStore.FILE_NAME + ".corrupt")));
        assertTrue(log.contains("could not be read"), log.dump());
        store.recordExecuted(new ExecutedStore.Executed("v1", "KICK", P, T), null, T);
        assertTrue(ExecutedStore.load(file(), log, T).executed("v1"));
    }

    @Test
    void noteUnbanKeepsTheNewest() {
        ExecutedStore store = ExecutedStore.load(file(), log, T);
        store.noteUnban(P, T - 10, T);
        store.noteUnban(P, T - 20, T);
        assertEquals(T - 10, store.lastUnban(P));
        store.noteUnban(null, T, T);
        store.noteUnban(P, 0, T);
        assertEquals(T - 10, store.lastUnban(P));
    }

    @Test
    void miniJsonRejectsMalformedInputAndParsesValues() {
        Object value = MiniJson.parse(" {\"a\": [1, -2, 3.5, true, false, null, \"x\\n\\u00e9\\\"\"], \"b\": {}} ");
        Map<?, ?> map = (Map<?, ?>) value;
        List<?> a = (List<?>) map.get("a");
        assertEquals(1L, a.get(0));
        assertEquals(-2L, a.get(1));
        assertEquals(3.5, a.get(2));
        assertEquals(Boolean.TRUE, a.get(3));
        assertNull(a.get(5));
        assertEquals("x\né\"", a.get(6));
        for (String bad : List.of("", "{", "[1,]", "{\"a\" 1}", "{} x", "\"\u0001\"", "[" .repeat(100) + "]".repeat(100),
                "tru", "{\"a\":\"\\q\"}")) {
            try {
                MiniJson.parse(bad);
                throw new AssertionError("accepted " + bad);
            } catch (IllegalArgumentException expected) {
                // ok
            }
        }
        StringBuilder out = new StringBuilder();
        MiniJson.quote(out, "a\"b\\c\n\u0001\u2028");
        assertEquals("a\"b\\c\n\u0001\u2028", MiniJson.parse(out.toString()));
    }
}
