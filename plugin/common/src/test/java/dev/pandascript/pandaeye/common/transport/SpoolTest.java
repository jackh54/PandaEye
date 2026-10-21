package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.time.EventClock;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.FileTime;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SpoolTest {

    @TempDir
    Path dir;

    private final EventClock clock = EventClock.system();
    private final TestLog log = new TestLog();

    @Test
    void roundTripsBatchesAndDeletesTheFileOnLoad() throws Exception {
        Spool spool = new Spool(dir, log, Spool.MAX_BYTES);
        PendingBatch sent = batch(new byte[]{1, 2, 3}, 3);
        sent.seq = 77;
        PendingBatch unsent = batch(new byte[]{4, 5}, 1);
        assertEquals(2, spool.write(List.of(sent, unsent), System.nanoTime() + 1_000_000_000L));

        List<PendingBatch> loaded = spool.load(clock);
        assertEquals(2, loaded.size());
        assertEquals(77, loaded.get(0).seq);
        assertArrayEquals(new byte[]{1, 2, 3}, loaded.get(0).events);
        assertEquals(3, loaded.get(0).eventCount);
        assertEquals(0, loaded.get(1).seq);
        assertTrue(Files.list(dir).findAny().isEmpty());
    }

    @Test
    void keepsTheNewestBatchesUnderTheSizeCap() {
        Spool spool = new Spool(dir, log, 3 * (100 + 24));
        List<PendingBatch> batches = new ArrayList<>();
        for (int i = 0; i < 5; i++) {
            batches.add(batch(new byte[100], i + 1));
        }
        assertEquals(3, spool.write(batches, System.nanoTime() + 1_000_000_000L));
        List<PendingBatch> loaded = spool.load(clock);
        assertEquals(List.of(3, 4, 5), loaded.stream().map(b -> b.eventCount).toList());
    }

    @Test
    void discardsFilesAndRecordsOlderThanAnHour() throws Exception {
        Spool spool = new Spool(dir, log, Spool.MAX_BYTES);
        PendingBatch old = new PendingBatch(new byte[]{1}, 1, 0, clock.nowMillis() - Spool.MAX_AGE_MILLIS - 1_000);
        PendingBatch fresh = batch(new byte[]{2}, 1);
        spool.write(List.of(old, fresh), System.nanoTime() + 1_000_000_000L);
        assertEquals(1, spool.load(clock).size());

        spool.write(List.of(batch(new byte[]{3}, 1)), System.nanoTime() + 1_000_000_000L);
        try (var files = Files.list(dir)) {
            for (Path file : files.toList()) {
                Files.setLastModifiedTime(file, FileTime.fromMillis(System.currentTimeMillis() - 2 * Spool.MAX_AGE_MILLIS));
            }
        }
        assertEquals(0, spool.load(clock).size());
        assertTrue(Files.list(dir).findAny().isEmpty());
    }

    @Test
    void ignoresCorruptFiles() throws Exception {
        Files.write(dir.resolve("spool-1.bin"), new byte[]{9, 9, 9, 9, 9});
        assertEquals(0, new Spool(dir, log, Spool.MAX_BYTES).load(clock).size());
        assertTrue(log.contains("unreadable spool file"));
    }

    private PendingBatch batch(byte[] events, int count) {
        return new PendingBatch(events, count, System.nanoTime(), clock.nowMillis());
    }
}
