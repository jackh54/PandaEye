package dev.pandascript.pandaeye.common.transport;

/** A point-in-time view of the transport for status commands. Times are microseconds; 0 = unknown. */
public record CloudStatus(
        CloudState state,
        String detail,
        String serverId,
        long connectionRttMicros,
        long lastProbeRttMicros,
        long probeP50Micros,
        long probeP95Micros,
        int pendingBatches,
        int replayBatches,
        long pendingBytes,
        int queuedEvents,
        long droppedEvents,
        long droppedBatches,
        long droppedBatchEvents,
        long oversizeEvents,
        long tooLargeBatches,
        long sentBatches,
        long lastAckedSeq,
        String compression) {
}
