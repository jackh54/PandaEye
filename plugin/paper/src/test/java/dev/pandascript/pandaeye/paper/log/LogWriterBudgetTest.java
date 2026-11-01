package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.common.Log;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Each log file has its own line budget and reports its own refused lines (ARCHITECTURE 4.9). */
class LogWriterBudgetTest {

    @TempDir
    Path dir;

    private static final Clock DAY = Clock.fixed(Instant.parse("2026-10-20T12:00:00Z"), ZoneOffset.UTC);
    private static final Pattern SUPPRESSED = Pattern.compile("\"suppressed_lines\":(\\d+)");

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
    void verdictsAndFlagsSurviveAPeaksBurst() throws Exception {
        LogWriter writer = new LogWriter(dir, QUIET, DAY);
        writer.start();
        int peaks = 3 * LogWriter.PEAKS_BURST;
        try {
            for (int i = 0; i < peaks; i++) {
                writer.peak(new Json().put("i", i));
                if (i % 300 == 0) { // 20 verdicts and 20 flags in the middle of the burst
                    writer.verdict(new Json().put("verdict", i));
                    writer.flag(new LogWriter.FlagRecord(UUID.randomUUID(), "p" + i, "movement.speed", 1, 1, "x",
                            i, System.nanoTime()));
                }
            }
            drain(writer);
        } finally {
            writer.stop(System.nanoTime() + TimeUnit.SECONDS.toNanos(1)); // flushes the flag aggregates
        }
        List<String> verdicts = lines("verdicts");
        assertEquals(20, verdicts.size(), "every verdict line written");
        assertTrue(verdicts.stream().noneMatch(line -> line.contains("suppressed_lines")));
        List<String> flags = lines("flags");
        assertEquals(20, flags.size(), "every flag line written");
        assertTrue(flags.stream().noneMatch(line -> line.contains("suppressed_lines")));

        List<String> peakLines = lines("peaks");
        long written = peakLines.stream().filter(line -> !line.contains("suppressed_lines")).count();
        assertTrue(written >= LogWriter.PEAKS_BURST && written < peaks, "peaks capped at its own budget: " + written);
        assertEquals(peaks, accounted(peakLines), "every refused peaks line is counted in the peaks file");
    }

    @Test
    void refusedLinesAreReportedInTheFileThatLostThemWithoutWaitingForItsNextLine() throws Exception {
        LogWriter writer = new LogWriter(dir, QUIET, DAY);
        writer.start();
        int verdicts = 200;
        try {
            for (int i = 0; i < verdicts; i++) {
                writer.verdict(new Json().put("i", i));
            }
            for (int i = 0; i < 10; i++) {
                writer.peak(new Json().put("i", i));
            }
            drain(writer);
            // no further verdict arrives: the count is written on its own once the budget refused nothing for 1 s
            waitUntil(() -> accounted(lines("verdicts")) == verdicts);
        } finally {
            writer.stop(System.nanoTime() + TimeUnit.SECONDS.toNanos(1));
        }
        List<String> verdictLines = lines("verdicts");
        assertTrue(suppressed(verdictLines) > 0, "the verdict budget refused lines");
        List<String> peakLines = lines("peaks");
        assertEquals(10, peakLines.size());
        assertTrue(peakLines.stream().noneMatch(line -> line.contains("suppressed_lines")),
                "the verdicts' count never lands in another file");
        assertFalse(Files.exists(file("flags")));
    }

    /** Returns once the writer thread handled everything offered before (the queue is FIFO). */
    private static void drain(LogWriter writer) throws Exception {
        CompletableFuture<LogWriter.ForgetResult> done = new CompletableFuture<>();
        assertTrue(writer.forget(UUID.randomUUID(), done::complete));
        done.get(10, TimeUnit.SECONDS);
    }

    private Path file(String kind) {
        return dir.resolve(kind + "-2026-10-20.jsonl");
    }

    private List<String> lines(String kind) {
        try {
            return Files.exists(file(kind)) ? Files.readAllLines(file(kind)) : List.of();
        } catch (IOException e) {
            throw new AssertionError(e);
        }
    }

    private static long suppressed(List<String> lines) {
        long total = 0;
        for (String line : lines) {
            Matcher matcher = SUPPRESSED.matcher(line);
            if (matcher.find()) {
                total += Long.parseLong(matcher.group(1));
            }
        }
        return total;
    }

    /** Lines written plus lines reported as refused. */
    private static long accounted(List<String> lines) {
        return lines.stream().filter(line -> !line.contains("suppressed_lines")).count() + suppressed(lines);
    }

    private static void waitUntil(java.util.function.BooleanSupplier condition) throws InterruptedException {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(10);
        while (!condition.getAsBoolean()) {
            if (System.nanoTime() > deadline) {
                throw new AssertionError("timed out");
            }
            Thread.sleep(20);
        }
    }
}
