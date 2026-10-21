package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.DaemonThreads;
import dev.pandascript.pandaeye.common.util.TokenBucket;

import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.AtomicMoveNotSupportedException;
import java.nio.file.StandardCopyOption;
import java.nio.file.StandardOpenOption;
import java.time.LocalDate;
import java.time.ZoneId;
import java.util.HashMap;
import java.util.Iterator;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.LongAdder;
import java.util.function.Consumer;

/**
 * The bounded local logs (ARCHITECTURE 4.9): {@code logs/flags-YYYY-MM-DD.jsonl} aggregated per player and
 * check per 5 s, {@code logs/peaks-YYYY-MM-DD.jsonl} with the 5-minute check statistics and
 * {@code logs/verdicts-YYYY-MM-DD.jsonl} with every verdict and what was done with it. One daemon
 * thread ({@code PandaEye-Log}) writes; producers only offer to a bounded queue (10 000). Global cap 50
 * lines/s (then one summary line), 256 MiB per file per day, no writes while free disk is below 1 GiB,
 * files kept 14 days.
 */
public final class LogWriter {

    static final int QUEUE_CAPACITY = 10_000;
    static final long MAX_FILE_BYTES = 256L << 20;
    static final long MIN_FREE_BYTES = 1L << 30;
    static final int KEEP_DAYS = 14;
    private static final long AGGREGATE_NANOS = 5_000_000_000L;
    private static final long DISK_CHECK_NANOS = 30_000_000_000L;
    private static final double LINES_PER_SECOND = 50;

    /** A confirmed flag to aggregate. */
    public record FlagRecord(UUID player, String name, String check, double severity, double vl, String info,
                             long unixMillis, long nanos) {
    }

    private record Line(String kind, String json) {
    }

    /** What {@link #forget} removed. */
    public record ForgetResult(int files, long lines, boolean failed) {
    }

    private record Forget(UUID player, Consumer<ForgetResult> done) {
    }

    private static final class Aggregate {
        final FlagRecord first;
        FlagRecord last;
        int count;
        double maxSeverity;

        Aggregate(FlagRecord first) {
            this.first = first;
            this.last = first;
        }
    }

    private final Path dir;
    private final Log log;
    private final ZoneId zone;
    private final ArrayBlockingQueue<Object> queue = new ArrayBlockingQueue<>(QUEUE_CAPACITY);
    private final LongAdder dropped = new LongAdder();
    private final DaemonThreads threads = DaemonThreads.named("PandaEye-Log");
    private final Map<String, Aggregate> aggregates = new HashMap<>();
    private final TokenBucket lineBudget = new TokenBucket(LINES_PER_SECOND, LINES_PER_SECOND, System.nanoTime());
    private ExecutorService executor;
    private volatile boolean running;
    private long suppressedLines;
    private long lastDiskCheck = Long.MIN_VALUE;
    private boolean diskFull;

    public LogWriter(Path dir, Log log, ZoneId zone) {
        this.dir = dir;
        this.log = log;
        this.zone = zone;
    }

    public void start() {
        running = true;
        executor = Executors.newSingleThreadExecutor(threads);
        executor.execute(this::run);
    }

    /** Stops the writer; pending aggregates are flushed if the deadline allows. */
    public void stop(long deadlineNanos) {
        running = false;
        if (executor == null) {
            return;
        }
        executor.shutdownNow();
        try {
            executor.awaitTermination(Math.max(0, deadlineNanos - System.nanoTime()), TimeUnit.NANOSECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        threads.joinAll(deadlineNanos);
    }

    public void flag(FlagRecord record) {
        offer(record);
    }

    /** A pre-built peaks line. */
    public void peak(Json line) {
        offer(new Line("peaks", line.toString()));
    }

    /** A handled verdict. */
    public void verdict(Json line) {
        offer(new Line("verdicts", line.toString()));
    }

    /**
     * {@code /pandaeye forget <uuid>}: removes every line about the player from the local log files (and drops
     * the player's pending aggregates) on the writer thread, then calls {@code done} there. Returns false when
     * the request could not be queued (writer stopped or queue full).
     */
    public boolean forget(UUID player, Consumer<ForgetResult> done) {
        return running && queue.offer(new Forget(player, done));
    }

    /** Lines (or flags) dropped because the queue was full, the rate cap hit, the disk is full or the file is full. */
    public long dropped() {
        return dropped.sum();
    }

    private void offer(Object item) {
        if (!running || !queue.offer(item)) {
            dropped.increment();
        }
    }

    private void run() {
        deleteOldFiles();
        try {
            while (running) {
                Object item = queue.poll(250, TimeUnit.MILLISECONDS);
                long now = System.nanoTime();
                if (item instanceof FlagRecord flag) {
                    aggregate(flag);
                } else if (item instanceof Line line) {
                    write(line, now);
                } else if (item instanceof Forget forget) {
                    forget(forget);
                }
                flushAggregates(now, false);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        flushAggregates(System.nanoTime(), true);
    }

    private void aggregate(FlagRecord flag) {
        String key = flag.player() + "|" + flag.check();
        Aggregate aggregate = aggregates.get(key);
        if (aggregate == null) {
            aggregate = new Aggregate(flag);
            aggregates.put(key, aggregate);
        }
        aggregate.last = flag;
        aggregate.count++;
        aggregate.maxSeverity = Math.max(aggregate.maxSeverity, flag.severity());
    }

    private void flushAggregates(long now, boolean all) {
        Iterator<Aggregate> it = aggregates.values().iterator();
        while (it.hasNext()) {
            Aggregate aggregate = it.next();
            if (all || now - aggregate.first.nanos() >= AGGREGATE_NANOS) {
                it.remove();
                FlagRecord first = aggregate.first;
                write(new Line("flags", new Json()
                        .put("time", first.unixMillis())
                        .put("last", aggregate.last.unixMillis())
                        .put("player", first.player().toString())
                        .put("name", first.name())
                        .put("check", first.check())
                        .put("count", aggregate.count)
                        .put("max_severity", aggregate.maxSeverity)
                        .put("vl", aggregate.last.vl())
                        .put("first_info", first.info())
                        .put("last_info", aggregate.last.info())
                        .toString()), now);
            }
        }
    }

    private void write(Line line, long now) {
        if (!lineBudget.tryAcquire(now, 1)) {
            suppressedLines++;
            dropped.increment();
            return;
        }
        String text = line.json();
        if (suppressedLines > 0) {
            text = new Json().put("time", System.currentTimeMillis()).put("suppressed_lines", suppressedLines)
                    + "\n" + text;
            suppressedLines = 0;
        }
        if (diskFull(now)) {
            dropped.increment();
            return;
        }
        Path file = dir.resolve(line.kind() + "-" + LocalDate.now(zone) + ".jsonl");
        try {
            Files.createDirectories(dir);
            if (Files.exists(file) && Files.size(file) >= MAX_FILE_BYTES) {
                dropped.increment();
                return;
            }
            try (BufferedWriter writer = Files.newBufferedWriter(file, StandardCharsets.UTF_8,
                    StandardOpenOption.CREATE, StandardOpenOption.APPEND)) {
                writer.write(text);
                writer.write('\n');
            }
        } catch (IOException e) {
            dropped.increment();
            log.warn("could not write " + file.getFileName() + ": " + e);
        }
    }

    private void forget(Forget request) {
        aggregates.values().removeIf(aggregate -> aggregate.first.player().equals(request.player()));
        String needle = "\"player\":\"" + request.player() + "\"";
        int files = 0;
        long lines = 0;
        boolean failed = false;
        if (Files.isDirectory(dir)) {
            try (DirectoryStream<Path> stream = Files.newDirectoryStream(dir, "*.jsonl")) {
                for (Path file : stream) {
                    long removed = removeLines(file, needle);
                    if (removed < 0) {
                        failed = true;
                    } else if (removed > 0) {
                        files++;
                        lines += removed;
                    }
                }
            } catch (IOException e) {
                failed = true;
                log.warn("could not list " + dir + " to forget a player: " + e);
            }
        }
        try {
            request.done().accept(new ForgetResult(files, lines, failed));
        } catch (Throwable t) {
            log.warn("forget callback failed", t);
        }
    }

    /** Rewrites {@code file} without the lines containing {@code needle}; returns how many, or -1 on failure. */
    private long removeLines(Path file, String needle) {
        Path tmp = file.resolveSibling(file.getFileName() + ".forget.tmp");
        long removed = 0;
        try {
            try (BufferedReader reader = Files.newBufferedReader(file, StandardCharsets.UTF_8);
                 BufferedWriter writer = Files.newBufferedWriter(tmp, StandardCharsets.UTF_8,
                         StandardOpenOption.CREATE, StandardOpenOption.TRUNCATE_EXISTING)) {
                String line;
                while ((line = reader.readLine()) != null) {
                    if (line.contains(needle)) {
                        removed++;
                    } else {
                        writer.write(line);
                        writer.write('\n');
                    }
                }
            }
            if (removed > 0) {
                try {
                    Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
                } catch (AtomicMoveNotSupportedException e) {
                    Files.move(tmp, file, StandardCopyOption.REPLACE_EXISTING);
                }
            } else {
                Files.deleteIfExists(tmp);
            }
            return removed;
        } catch (IOException e) {
            log.warn("could not remove a player's lines from " + file.getFileName() + ": " + e);
            try {
                Files.deleteIfExists(tmp);
            } catch (IOException ignored) {
                // best effort
            }
            return -1;
        }
    }

    private boolean diskFull(long now) {
        if (lastDiskCheck == Long.MIN_VALUE || now - lastDiskCheck > DISK_CHECK_NANOS) {
            lastDiskCheck = now;
            try {
                Files.createDirectories(dir);
                diskFull = Files.getFileStore(dir).getUsableSpace() < MIN_FREE_BYTES;
            } catch (IOException e) {
                diskFull = false;
            }
        }
        return diskFull;
    }

    private void deleteOldFiles() {
        if (!Files.isDirectory(dir)) {
            return;
        }
        LocalDate cutoff = LocalDate.now(zone).minusDays(KEEP_DAYS);
        try (DirectoryStream<Path> files = Files.newDirectoryStream(dir, "*.jsonl")) {
            for (Path file : files) {
                String name = file.getFileName().toString();
                int dash = name.indexOf('-');
                try {
                    LocalDate date = LocalDate.parse(name.substring(dash + 1, name.length() - ".jsonl".length()));
                    if (date.isBefore(cutoff)) {
                        Files.deleteIfExists(file);
                    }
                } catch (RuntimeException ignored) {
                    // not one of ours
                }
            }
        } catch (IOException e) {
            log.warn("could not clean old logs: " + e);
        }
    }
}
