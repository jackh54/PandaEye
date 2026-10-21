package dev.pandascript.pandaeye.common.transport;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PendingBufferTest {

    private static final long SECOND = 1_000_000_000L;

    @Test
    void dropsTheOldestBatchWhenOverTheByteLimit() {
        PendingBuffer buffer = new PendingBuffer(250, 600 * SECOND);
        PendingBatch a = batch(100, 1);
        PendingBatch b = batch(100, 2);
        PendingBatch c = batch(100, 3);
        buffer.add(a, false);
        buffer.add(b, true);
        buffer.add(c, true);
        assertEquals(2, buffer.batchCount());
        assertEquals(200, buffer.bytes());
        assertEquals(1, buffer.droppedBatches());
        assertEquals(a.eventCount, buffer.droppedEvents());
        assertSame(b, buffer.pollLive());
    }

    @Test
    void expiresBatchesOlderThanTheMaximumAge() {
        PendingBuffer buffer = new PendingBuffer(1 << 20, 10 * SECOND);
        buffer.add(batch(10, 0), false);
        buffer.add(batch(10, 5 * SECOND), true);
        PendingBatch sent = batch(10, 1);
        sent.seq = 9;
        buffer.markSent(sent, 1);
        buffer.expire(12 * SECOND);
        assertEquals(1, buffer.batchCount());
        assertEquals(2, buffer.droppedBatches());
    }

    @Test
    void reconnectDropsAcknowledgedBatchesAndTurnsTheRestIntoReplay() {
        PendingBuffer buffer = new PendingBuffer(1 << 20, 600 * SECOND);
        PendingBatch s1 = sent(buffer, 1, 1);
        PendingBatch s2 = sent(buffer, 2, 1);
        PendingBatch live = batch(10, 3);
        buffer.add(live, true);
        assertFalse(live.replay);

        buffer.onConnected(1);
        assertEquals(1, buffer.inflightCount());
        assertTrue(s2.replay);
        assertSame(s2, buffer.nextResend(2));
        assertNull(buffer.pollLive(), "live batches built before the reconnect become replay");
        assertSame(live, buffer.peekReplay());
        assertTrue(live.replay);
        assertFalse(s1.replay);
    }

    @Test
    void resendsInSeqOrderOncePerConnection() {
        PendingBuffer buffer = new PendingBuffer(1 << 20, 600 * SECOND);
        PendingBatch late = sent(buffer, 7, 1);
        PendingBatch early = sent(buffer, 5, 1);
        assertSame(early, buffer.nextResend(2));
        buffer.markSent(early, 2);
        assertSame(late, buffer.nextResend(2));
        buffer.markSent(late, 2);
        assertNull(buffer.nextResend(2));
    }

    @Test
    void ackRemovesEverythingUpToTheSeq() {
        PendingBuffer buffer = new PendingBuffer(1 << 20, 600 * SECOND);
        sent(buffer, 1, 1);
        sent(buffer, 2, 1);
        sent(buffer, 3, 1);
        buffer.ack(2);
        assertEquals(1, buffer.inflightCount());
        assertEquals(10, buffer.bytes());
    }

    @Test
    void restoredBatchesKeepTheirSeqAndDrainInSendOrder() {
        PendingBuffer buffer = new PendingBuffer(1 << 20, 600 * SECOND);
        PendingBatch assigned = batch(10, 1);
        assigned.seq = 40;
        PendingBatch unsent = batch(10, 2);
        buffer.restore(List.of(unsent, assigned));
        assertSame(assigned, buffer.nextResend(1));
        assertSame(unsent, buffer.peekReplay());
        buffer.add(batch(10, 3), true);
        List<PendingBatch> all = buffer.drainAll();
        assertEquals(3, all.size());
        assertSame(assigned, all.get(0));
        assertSame(unsent, all.get(1));
        assertEquals(0, buffer.bytes());
    }

    private static PendingBatch sent(PendingBuffer buffer, long seq, int generation) {
        PendingBatch batch = batch(10, seq);
        buffer.add(batch, true);
        assertSame(batch, buffer.pollLive());
        batch.seq = seq;
        buffer.markSent(batch, generation);
        return batch;
    }

    private static PendingBatch batch(int size, long createdNanos) {
        return new PendingBatch(new byte[size], 2, createdNanos, createdNanos / 1_000_000);
    }
}
