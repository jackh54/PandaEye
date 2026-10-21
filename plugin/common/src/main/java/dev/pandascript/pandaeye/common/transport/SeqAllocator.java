package dev.pandascript.pandaeye.common.transport;

/**
 * Batch sequence numbers, reserved in persisted blocks of {@value #BLOCK} so a restart (clean or not) always
 * resumes above every seq that may have been used. Cloud thread only.
 */
final class SeqAllocator {

    static final long BLOCK = 100_000;

    private final StateStore store;
    private long next;
    private long blockEnd;

    SeqAllocator(StateStore store) {
        this.store = store;
        this.next = store.seqBlockEnd() + 1;
        reserve();
    }

    long next() {
        if (next > blockEnd) {
            reserve();
        }
        return next++;
    }

    /** Called with Welcome.last_acked_seq: never hand out a seq the cloud would treat as a duplicate. */
    void ensureAbove(long seq) {
        if (next <= seq) {
            next = seq + 1;
            reserve();
        }
    }

    long peekNext() {
        return next;
    }

    private void reserve() {
        blockEnd = next + BLOCK - 1;
        store.saveSeqBlockEnd(blockEnd);
    }
}
