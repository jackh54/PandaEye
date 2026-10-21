package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.common.Log;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.LocalDate;
import java.time.ZoneId;
import java.time.ZoneOffset;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Files older than 14 days are deleted at start and at every day rollover, not only at start. */
class LogWriterRetentionTest {

    @TempDir
    Path dir;

    private static final class MovableClock extends Clock {
        volatile Instant now;

        MovableClock(Instant now) {
            this.now = now;
        }

        @Override
        public ZoneId getZone() {
            return ZoneOffset.UTC;
        }

        @Override
        public Clock withZone(ZoneId zone) {
            return this;
        }

        @Override
        public Instant instant() {
            return now;
        }
    }

    private static final Log QUIET = new Log() {
        @Override
        public void info(String message) {
        }

        @Override
        public void warn(String message) {
        }

        @Override
        public void warn(String message, Throwable error) {
        }

        @Override
        public void debug(String message) {
        }
    };

    @Test
    void oldFilesAreDeletedAtTheDayRollover() throws Exception {
        MovableClock clock = new MovableClock(Instant.parse("2026-03-20T23:59:00Z"));
        LocalDate today = LocalDate.of(2026, 3, 20);
        Path tooOld = dir.resolve("flags-" + today.minusDays(15) + ".jsonl");
        Path keptToday = dir.resolve("flags-" + today.minusDays(14) + ".jsonl");
        Files.writeString(tooOld, "{}\n");
        Files.writeString(keptToday, "{}\n");
        LogWriter writer = new LogWriter(dir, QUIET, clock);
        writer.start();
        try {
            waitUntil(() -> !Files.exists(tooOld));
            Thread.sleep(300);
            assertTrue(Files.exists(keptToday), "exactly 14 days old is kept");

            clock.now = clock.now.plus(Duration.ofMinutes(2)); // midnight passes while running
            waitUntil(() -> !Files.exists(keptToday));
            assertFalse(Files.exists(keptToday));
        } finally {
            writer.stop(System.nanoTime() + TimeUnit.SECONDS.toNanos(1));
        }
    }

    private static void waitUntil(java.util.function.BooleanSupplier condition) throws InterruptedException {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
        while (!condition.getAsBoolean()) {
            if (System.nanoTime() > deadline) {
                throw new AssertionError("timed out");
            }
            Thread.sleep(20);
        }
    }
}
