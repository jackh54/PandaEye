package dev.pandascript.pandaeye.common.transport;

import com.google.protobuf.CodedOutputStream;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.Event;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.util.ArrayList;
import java.util.List;

/** Drains the {@link EventSink} into pre-serialised batches within the protocol limits. */
final class BatchBuilder {

    static final int MAX_BATCH_BYTES = 512 * 1024;
    /** The gateway rejects batches containing an event above this size. */
    static final int MAX_EVENT_BYTES = 64 * 1024;

    private final java.util.concurrent.atomic.AtomicLong oversizeDropped = new java.util.concurrent.atomic.AtomicLong();

    /** Returns the next batch of at most {@code maxEvents} events and 512 KiB, or null if the sink is empty. */
    PendingBatch next(EventSink sink, int maxEvents, long nowNanos, long nowUnixMs) {
        List<Event> events = new ArrayList<>();
        int bytes = 0;
        while (events.size() < maxEvents) {
            Event event = sink.peek();
            if (event == null) {
                break;
            }
            if (event.getSerializedSize() > MAX_EVENT_BYTES) {
                sink.poll();
                oversizeDropped.incrementAndGet();
                continue;
            }
            int size = CodedOutputStream.computeMessageSize(Batch.EVENTS_FIELD_NUMBER, event);
            if (bytes + size > MAX_BATCH_BYTES) {
                break;
            }
            sink.poll();
            events.add(event);
            bytes += size;
        }
        if (events.isEmpty()) {
            return null;
        }
        byte[] out = new byte[bytes];
        CodedOutputStream stream = CodedOutputStream.newInstance(out);
        try {
            for (Event event : events) {
                stream.writeMessage(Batch.EVENTS_FIELD_NUMBER, event);
            }
            stream.checkNoSpaceLeft();
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        return new PendingBatch(out, events.size(), nowNanos, nowUnixMs);
    }

    long oversizeDropped() {
        return oversizeDropped.get();
    }
}
