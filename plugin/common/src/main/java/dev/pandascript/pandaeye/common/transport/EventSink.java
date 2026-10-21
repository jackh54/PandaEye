package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.protocol.v1.Event;

import java.util.concurrent.ConcurrentLinkedQueue;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.LongAdder;

/**
 * Bounded multi-producer single-consumer event queue between the game threads and the cloud thread. When it
 * is full the newest event is dropped and counted; producers never block.
 */
public final class EventSink {

    public static final int DEFAULT_CAPACITY = 100_000;

    private final ConcurrentLinkedQueue<Event> queue = new ConcurrentLinkedQueue<>();
    private final AtomicInteger size = new AtomicInteger();
    private final LongAdder dropped = new LongAdder();
    private final int capacity;

    public EventSink(int capacity) {
        this.capacity = capacity;
    }

    /** Returns false (and counts a drop) when the queue is full. */
    public boolean offer(Event event) {
        if (size.incrementAndGet() > capacity) {
            size.decrementAndGet();
            dropped.increment();
            return false;
        }
        queue.offer(event);
        return true;
    }

    /** Consumer side only. */
    Event peek() {
        return queue.peek();
    }

    /** Consumer side only. */
    Event poll() {
        Event event = queue.poll();
        if (event != null) {
            size.decrementAndGet();
        }
        return event;
    }

    public int size() {
        return size.get();
    }

    public long dropped() {
        return dropped.sum();
    }
}
