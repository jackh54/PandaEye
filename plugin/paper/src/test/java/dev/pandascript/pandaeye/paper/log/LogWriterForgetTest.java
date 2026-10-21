package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.common.Log;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.ZoneId;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** {@code /pandaeye forget <uuid>} removes a player's lines from the local logs (ARCHITECTURE 4.9). */
class LogWriterForgetTest {

    @TempDir
    Path dir;

    private final Log log = new Log() {
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
    void removesOnlyThatPlayersLines() throws Exception {
        UUID gone = UUID.randomUUID();
        UUID kept = UUID.randomUUID();
        Path flags = dir.resolve("flags-2026-10-20.jsonl");
        Path verdicts = dir.resolve("verdicts-2026-10-20.jsonl");
        Path other = dir.resolve("peaks-2026-10-20.jsonl");
        Files.writeString(flags, line(gone, "a") + line(kept, "b") + line(gone, "c"));
        Files.writeString(verdicts, line(gone, "v"));
        Files.writeString(other, line(kept, "p"));
        LogWriter writer = new LogWriter(dir, log, ZoneId.of("UTC"));
        writer.start();
        try {
            CompletableFuture<LogWriter.ForgetResult> done = new CompletableFuture<>();
            assertTrue(writer.forget(gone, done::complete));
            LogWriter.ForgetResult result = done.get(10, TimeUnit.SECONDS);
            assertEquals(2, result.files());
            assertEquals(3, result.lines());
            assertFalse(result.failed());
        } finally {
            writer.stop(System.nanoTime() + TimeUnit.SECONDS.toNanos(1));
        }
        assertEquals(List.of(line(kept, "b").trim()), Files.readAllLines(flags));
        assertEquals(List.of(), Files.readAllLines(verdicts));
        assertEquals(List.of(line(kept, "p").trim()), Files.readAllLines(other));
        try (var files = Files.list(dir)) {
            assertTrue(files.noneMatch(f -> f.getFileName().toString().endsWith(".tmp")));
        }
    }

    @Test
    void refusesWhenStopped() {
        LogWriter writer = new LogWriter(dir, log, ZoneId.of("UTC"));
        assertFalse(writer.forget(UUID.randomUUID(), result -> {
        }));
    }

    private static String line(UUID player, String info) {
        return new Json().put("time", 1L).put("player", player.toString()).put("info", info) + "\n";
    }
}
