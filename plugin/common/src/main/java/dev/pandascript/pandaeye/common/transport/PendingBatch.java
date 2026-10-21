package dev.pandascript.pandaeye.common.transport;

/**
 * One batch of events waiting for its BatchAck. The events are kept pre-serialised (a {@code Batch} message
 * holding only field 4) so the seq, send time and replay bit can be prepended at send time without
 * re-encoding. Mutable fields are owned by the cloud thread.
 */
final class PendingBatch {

    final byte[] events;
    final int eventCount;
    final long createdNanos;
    final long createdUnixMs;
    /** 0 until the batch is first sent. */
    long seq;
    boolean replay;
    /** Connection generation the batch was last sent on; -1 = never. */
    int sentGeneration = -1;

    PendingBatch(byte[] events, int eventCount, long createdNanos, long createdUnixMs) {
        this.events = events;
        this.eventCount = eventCount;
        this.createdNanos = createdNanos;
        this.createdUnixMs = createdUnixMs;
    }

    int sizeBytes() {
        return events.length;
    }
}
