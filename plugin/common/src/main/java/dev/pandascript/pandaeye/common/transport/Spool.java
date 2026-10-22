package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.EOFException;
import java.io.IOException;
import java.nio.file.AtomicMoveNotSupportedException;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.NoSuchFileException;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.List;

/**
 * Unacknowledged batches written to disk on shutdown and resent (as replayed) after the next start. Bounded
 * by total size and age; a file is deleted once loaded. Record format (big-endian): magic {@code PES1}, then
 * repeated {@code seq:int64, createdUnixMs:int64, eventCount:int32, length:int32, events:bytes}.
 */
final class Spool {

    static final long MAX_BYTES = 64L << 20;
    static final long MAX_AGE_MILLIS = 3_600_000L;
    private static final int MAGIC = 0x50455331;
    private static final String PREFIX = "spool-";
    private static final String SUFFIX = ".bin";

    private final Path dir;
    private final Log log;
    private final long maxBytes;

    Spool(Path dir, Log log, long maxBytes) {
        this.dir = dir;
        this.log = log;
        this.maxBytes = maxBytes;
    }

    /** Loads and deletes every spool file; drops records older than an hour. */
    List<PendingBatch> load(EventClock clock) {
        List<PendingBatch> loaded = new ArrayList<>();
        if (!Files.isDirectory(dir)) {
            return loaded;
        }
        long nowMs = clock.nowMillis();
        long total = 0;
        for (Path file : spoolFiles()) {
            try {
                if (nowMs - Files.getLastModifiedTime(file).toMillis() <= MAX_AGE_MILLIS) {
                    total = read(file, clock, nowMs, loaded, total);
                }
            } catch (IOException e) {
                log.warn("ignoring unreadable spool file " + file.getFileName() + ": " + e);
            } finally {
                deleteQuietly(file);
            }
        }
        return loaded;
    }

    /**
     * Writes the newest batches that fit under the size cap, oldest first, stopping at the deadline.
     * Returns the number of batches written.
     */
    int write(List<PendingBatch> batches, long deadlineNanos) {
        if (batches.isEmpty()) {
            return 0;
        }
        long budget = maxBytes - existingBytes();
        int first = batches.size();
        long size = 0;
        while (first > 0 && size + batches.get(first - 1).sizeBytes() + 24 <= budget) {
            first--;
            size += batches.get(first).sizeBytes() + 24;
        }
        if (first == batches.size()) {
            log.warn("spool is full; dropping " + batches.size() + " unsent batches");
            return 0;
        }
        int written = 0;
        try {
            Files.createDirectories(dir);
            Path target = dir.resolve(PREFIX + System.currentTimeMillis() + SUFFIX);
            Path tmp = dir.resolve(target.getFileName() + ".tmp");
            try (DataOutputStream out = new DataOutputStream(new BufferedOutputStream(Files.newOutputStream(tmp), 1 << 16))) {
                out.writeInt(MAGIC);
                for (int i = first; i < batches.size() && System.nanoTime() - deadlineNanos < 0; i++) {
                    PendingBatch batch = batches.get(i);
                    out.writeLong(batch.seq);
                    out.writeLong(batch.createdUnixMs);
                    out.writeInt(batch.eventCount);
                    out.writeInt(batch.events.length);
                    out.write(batch.events);
                    written++;
                }
            }
            moveIntoPlace(tmp, target);
        } catch (IOException e) {
            log.warn("could not write the spool: " + e);
            return 0;
        }
        int dropped = batches.size() - written;
        if (dropped > 0) {
            log.warn("spooled " + written + " batches, dropped " + dropped + " (size cap or shutdown deadline)");
        }
        return written;
    }

    private long read(Path file, EventClock clock, long nowMs, List<PendingBatch> into, long total) throws IOException {
        try (DataInputStream in = new DataInputStream(new BufferedInputStream(Files.newInputStream(file), 1 << 16))) {
            if (in.readInt() != MAGIC) {
                throw new IOException("bad magic");
            }
            while (true) {
                long seq;
                try {
                    seq = in.readLong();
                } catch (EOFException end) {
                    return total;
                }
                long createdMs = in.readLong();
                int eventCount = in.readInt();
                int length = in.readInt();
                if (seq < 0 || eventCount <= 0 || length <= 0 || length > BatchBuilder.MAX_BATCH_BYTES) {
                    throw new IOException("corrupt record");
                }
                byte[] events = in.readNBytes(length);
                if (events.length != length) {
                    return total;
                }
                if (nowMs - createdMs > MAX_AGE_MILLIS || total + length > maxBytes) {
                    continue;
                }
                PendingBatch batch = new PendingBatch(events, eventCount, clock.unixMillisToNanos(createdMs), createdMs);
                batch.seq = seq;
                into.add(batch);
                total += length;
            }
        }
    }

    private List<Path> spoolFiles() {
        List<Path> files = new ArrayList<>();
        try (DirectoryStream<Path> stream = Files.newDirectoryStream(dir, PREFIX + "*" + SUFFIX)) {
            stream.forEach(files::add);
        } catch (NoSuchFileException e) {
            // Nothing was ever spooled: the directory is created on the first spill.
        } catch (IOException e) {
            log.warn("could not list the spool directory: " + e);
        }
        files.sort(null);
        return files;
    }

    private long existingBytes() {
        long total = 0;
        for (Path file : spoolFiles()) {
            try {
                total += Files.size(file);
            } catch (IOException ignored) {
                // a file that vanished does not count
            }
        }
        return total;
    }

    private static void moveIntoPlace(Path tmp, Path target) throws IOException {
        try {
            Files.move(tmp, target, StandardCopyOption.ATOMIC_MOVE);
        } catch (AtomicMoveNotSupportedException e) {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING);
        }
    }

    private void deleteQuietly(Path file) {
        try {
            Files.deleteIfExists(file);
        } catch (IOException e) {
            log.warn("could not delete spool file " + file.getFileName() + ": " + e);
        }
    }
}
