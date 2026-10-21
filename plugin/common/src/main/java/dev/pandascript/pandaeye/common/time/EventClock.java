package dev.pandascript.pandaeye.common.time;

import java.time.Instant;
import java.util.function.LongSupplier;

/**
 * The plugin event clock: Unix microseconds derived from the monotonic {@link System#nanoTime()} so event
 * times never go backwards and are comparable across all events of one plugin process.
 */
public final class EventClock {

    private final long unixMicrosAtStart;
    private final long nanoAtStart;
    private final LongSupplier nanoSource;

    public EventClock(long unixMicrosAtStart, long nanoAtStart, LongSupplier nanoSource) {
        this.unixMicrosAtStart = unixMicrosAtStart;
        this.nanoAtStart = nanoAtStart;
        this.nanoSource = nanoSource;
    }

    public static EventClock system() {
        Instant now = Instant.now();
        long micros = now.getEpochSecond() * 1_000_000L + now.getNano() / 1_000L;
        return new EventClock(micros, System.nanoTime(), System::nanoTime);
    }

    /** Current monotonic time in nanoseconds (the source the clock is based on). */
    public long nanoTime() {
        return nanoSource.getAsLong();
    }

    /** Current event time in Unix microseconds. */
    public long nowMicros() {
        return toMicros(nanoSource.getAsLong());
    }

    /** Current event time in Unix milliseconds. */
    public long nowMillis() {
        return nowMicros() / 1_000L;
    }

    /** Converts a {@link System#nanoTime()} reading taken in this process to event-clock microseconds. */
    public long toMicros(long nanoTime) {
        return unixMicrosAtStart + (nanoTime - nanoAtStart) / 1_000L;
    }

    /** Converts event-clock Unix milliseconds back to this process's nanoTime scale. */
    public long unixMillisToNanos(long unixMillis) {
        return nanoAtStart + (unixMillis * 1_000L - unixMicrosAtStart) * 1_000L;
    }
}
