package dev.pandascript.pandaeye.common.transport;

import com.google.protobuf.ByteString;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.LocalFlag;
import org.junit.jupiter.api.Test;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class BatchingTest {

    @Test
    void sinkDropsNewestWhenFullAndCounts() {
        EventSink sink = new EventSink(2);
        assertTrue(sink.offer(event(1)));
        assertTrue(sink.offer(event(2)));
        assertFalse(sink.offer(event(3)));
        assertEquals(1, sink.dropped());
        assertEquals(1, sink.poll().getTick());
        assertTrue(sink.offer(event(4)));
        assertEquals(2, sink.size());
    }

    @Test
    void sinkStaysBoundedUnderConcurrentProducers() throws Exception {
        EventSink sink = new EventSink(1_000);
        int producers = 8;
        CountDownLatch done = new CountDownLatch(producers);
        AtomicInteger accepted = new AtomicInteger();
        for (int p = 0; p < producers; p++) {
            new Thread(() -> {
                for (int i = 0; i < 500; i++) {
                    if (sink.offer(event(i))) {
                        accepted.incrementAndGet();
                    }
                }
                done.countDown();
            }).start();
        }
        done.await();
        assertEquals(1_000, accepted.get());
        assertEquals(1_000, sink.size());
        assertEquals(3_000, sink.dropped());
    }

    @Test
    void batchesRespectTheEventCountAndPreserveOrder() throws Exception {
        EventSink sink = new EventSink(100);
        for (int i = 0; i < 10; i++) {
            sink.offer(event(i));
        }
        BatchBuilder builder = new BatchBuilder();
        PendingBatch first = builder.next(sink, 4, 1, 2);
        assertEquals(4, first.eventCount);
        first.seq = 12;
        Batch decoded = ClientFrame.parseFrom(Frames.batch(first, 99)).getBatch();
        assertEquals(12, decoded.getSeq());
        assertEquals(99, decoded.getSentUnixMs());
        assertFalse(decoded.getReplayed());
        assertEquals(java.util.List.of(0, 1, 2, 3), decoded.getEventsList().stream().map(Event::getTick).toList());
        assertEquals(4, builder.next(sink, 4, 1, 2).eventCount);
        assertEquals(2, builder.next(sink, 4, 1, 2).eventCount);
        assertNull(builder.next(sink, 4, 1, 2));
    }

    @Test
    void batchesStayBelow512KiBAndOversizedEventsAreDropped() throws Exception {
        EventSink sink = new EventSink(100);
        sink.offer(big(BatchBuilder.MAX_EVENT_BYTES + 1));
        for (int i = 0; i < 20; i++) {
            sink.offer(big(60_000));
        }
        BatchBuilder builder = new BatchBuilder();
        PendingBatch batch = builder.next(sink, 4_096, 1, 2);
        assertEquals(1, builder.oversizeDropped());
        assertTrue(batch.events.length <= BatchBuilder.MAX_BATCH_BYTES);
        assertEquals(8, batch.eventCount);
        batch.seq = 1;
        batch.replay = true;
        Batch decoded = ClientFrame.parseFrom(Frames.batch(batch, 5)).getBatch();
        assertEquals(8, decoded.getEventsCount());
        assertTrue(decoded.getReplayed());
    }

    private static Event event(int tick) {
        return Event.newBuilder().setPlayer(ByteString.copyFrom(new byte[16])).setTick(tick).setTimeUs(tick).build();
    }

    private static Event big(int infoBytes) {
        return Event.newBuilder().setFlag(LocalFlag.newBuilder().setInfo("x".repeat(infoBytes))).build();
    }
}
