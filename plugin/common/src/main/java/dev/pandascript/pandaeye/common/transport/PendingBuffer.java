package dev.pandascript.pandaeye.common.transport;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collection;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * Every batch the cloud has not acknowledged yet, bounded by total bytes and age (the oldest batch is dropped
 * and counted). Three groups:
 * <ul>
 *   <li>in flight: sent at least once, so their seq is fixed; resent first after a reconnect, in seq order,
 *       which keeps the per-partition seq the workers see monotonic;</li>
 *   <li>live: built while connected and not sent yet;</li>
 *   <li>replay: built while disconnected (or restored from the spool); sent after live batches, paced.</li>
 * </ul>
 * Owned by the cloud thread; synchronised so shutdown can spool it from another thread.
 */
final class PendingBuffer {

    private final TreeMap<Long, PendingBatch> inflight = new TreeMap<>();
    private final ArrayDeque<PendingBatch> live = new ArrayDeque<>();
    private final ArrayDeque<PendingBatch> replay = new ArrayDeque<>();
    private final long maxBytes;
    private final long maxAgeNanos;
    private long bytes;
    private long droppedBatches;
    private long droppedEvents;

    PendingBuffer(long maxBytes, long maxAgeNanos) {
        this.maxBytes = maxBytes;
        this.maxAgeNanos = maxAgeNanos;
    }

    /** A freshly built batch: live when a connection is ready, otherwise part of the replay backlog. */
    synchronized void add(PendingBatch batch, boolean connected) {
        batch.replay = !connected;
        (connected ? live : replay).addLast(batch);
        bytes += batch.sizeBytes();
        trim();
    }

    /** Batches restored from the spool; they keep any seq they were already sent with. */
    synchronized void restore(Collection<PendingBatch> batches) {
        for (PendingBatch batch : batches) {
            batch.replay = true;
            if (batch.seq != 0) {
                inflight.put(batch.seq, batch);
            } else {
                replay.addLast(batch);
            }
            bytes += batch.sizeBytes();
        }
        trim();
    }

    /**
     * A new connection was accepted: drop what the cloud already holds, and turn everything built before it
     * into replayed data.
     */
    synchronized void onConnected(long lastAckedSeq) {
        ack(lastAckedSeq);
        for (PendingBatch batch : inflight.values()) {
            batch.replay = true;
        }
        while (!live.isEmpty()) {
            PendingBatch batch = live.pollFirst();
            batch.replay = true;
            replay.addLast(batch);
        }
    }

    /** Every batch with seq <= {@code seq} is durable. */
    synchronized void ack(long seq) {
        Iterator<Map.Entry<Long, PendingBatch>> it = inflight.headMap(seq, true).entrySet().iterator();
        while (it.hasNext()) {
            bytes -= it.next().getValue().sizeBytes();
            it.remove();
        }
    }

    /** The lowest-seq in-flight batch not yet sent on this connection generation. */
    synchronized PendingBatch nextResend(int generation) {
        for (PendingBatch batch : inflight.values()) {
            if (batch.sentGeneration != generation) {
                return batch;
            }
        }
        return null;
    }

    synchronized PendingBatch pollLive() {
        return live.pollFirst();
    }

    synchronized PendingBatch peekReplay() {
        return replay.peekFirst();
    }

    synchronized PendingBatch pollReplay() {
        return replay.pollFirst();
    }

    /** Records that {@code batch} (with its seq assigned) is being sent on {@code generation}. */
    synchronized void markSent(PendingBatch batch, int generation) {
        if (batch.seq == 0) {
            throw new IllegalStateException("batch sent without a seq");
        }
        batch.sentGeneration = generation;
        inflight.put(batch.seq, batch);
    }

    synchronized int inflightCount() {
        return inflight.size();
    }

    /** Drops batches older than the configured maximum age. */
    synchronized void expire(long nowNanos) {
        long cutoff = nowNanos - maxAgeNanos;
        dropOlderThan(replay, cutoff);
        dropOlderThan(live, cutoff);
        Iterator<PendingBatch> it = inflight.values().iterator();
        while (it.hasNext()) {
            PendingBatch batch = it.next();
            if (batch.createdNanos - cutoff < 0) {
                it.remove();
                countDrop(batch);
            }
        }
    }

    /** Removes and returns everything, in-flight batches first (seq order), then replay, then live. */
    synchronized List<PendingBatch> drainAll() {
        List<PendingBatch> all = new ArrayList<>(inflight.size() + replay.size() + live.size());
        all.addAll(inflight.values());
        all.addAll(replay);
        all.addAll(live);
        inflight.clear();
        replay.clear();
        live.clear();
        bytes = 0;
        return all;
    }

    synchronized int batchCount() {
        return inflight.size() + live.size() + replay.size();
    }

    synchronized int replayCount() {
        return replay.size();
    }

    synchronized long bytes() {
        return bytes;
    }

    synchronized long droppedBatches() {
        return droppedBatches;
    }

    synchronized long droppedEvents() {
        return droppedEvents;
    }

    private void trim() {
        while (bytes > maxBytes) {
            PendingBatch oldest = oldest();
            if (oldest == null) {
                return;
            }
            if (oldest == replay.peekFirst()) {
                replay.pollFirst();
            } else if (oldest == live.peekFirst()) {
                live.pollFirst();
            } else {
                inflight.remove(oldest.seq);
            }
            countDrop(oldest);
        }
    }

    private PendingBatch oldest() {
        PendingBatch oldest = replay.peekFirst();
        oldest = older(oldest, live.peekFirst());
        Map.Entry<Long, PendingBatch> first = inflight.firstEntry();
        return older(oldest, first == null ? null : first.getValue());
    }

    private static PendingBatch older(PendingBatch a, PendingBatch b) {
        if (a == null) {
            return b;
        }
        if (b == null) {
            return a;
        }
        return b.createdNanos - a.createdNanos < 0 ? b : a;
    }

    private void dropOlderThan(ArrayDeque<PendingBatch> queue, long cutoff) {
        while (!queue.isEmpty() && queue.peekFirst().createdNanos - cutoff < 0) {
            countDrop(queue.pollFirst());
        }
    }

    private void countDrop(PendingBatch batch) {
        bytes -= batch.sizeBytes();
        droppedBatches++;
        droppedEvents += batch.eventCount;
    }
}
