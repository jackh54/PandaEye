package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReferenceArray;

/**
 * The last {@value #SIZE} positions received from one player, readable from any thread without locks: the
 * owner (the player's netty thread) publishes immutable entries; readers (other players' attack handling)
 * walk back from the published head.
 */
public final class PositionHistory {

    public static final int SIZE = 64;

    public record Entry(long timeMicros, double x, double y, double z) {
    }

    private final AtomicReferenceArray<Entry> ring = new AtomicReferenceArray<>(SIZE);
    private final AtomicLong head = new AtomicLong();

    /** Owner thread only. */
    void add(long timeMicros, double x, double y, double z) {
        long index = head.get();
        ring.set((int) (index % SIZE), new Entry(timeMicros, x, y, z));
        head.set(index + 1);
    }

    void clear() {
        long index = head.get();
        for (int i = 0; i < SIZE; i++) {
            ring.set(i, null);
        }
        head.set(index);
    }

    /** Entries received at or after {@code sinceMicros}, oldest first, at most {@code max}. Any thread. */
    public List<Entry> since(long sinceMicros, int max) {
        long end = head.get();
        List<Entry> newestFirst = new ArrayList<>();
        for (long i = end - 1; i >= 0 && i >= end - SIZE && newestFirst.size() < max; i--) {
            Entry entry = ring.get((int) (i % SIZE));
            if (entry == null || entry.timeMicros() < sinceMicros) {
                break;
            }
            newestFirst.add(entry);
        }
        List<Entry> oldestFirst = new ArrayList<>(newestFirst.size());
        for (int i = newestFirst.size() - 1; i >= 0; i--) {
            oldestFirst.add(newestFirst.get(i));
        }
        return oldestFirst;
    }

    /** The newest entry, or null. Any thread. */
    public Entry latest() {
        long end = head.get();
        return end == 0 ? null : ring.get((int) ((end - 1) % SIZE));
    }
}
