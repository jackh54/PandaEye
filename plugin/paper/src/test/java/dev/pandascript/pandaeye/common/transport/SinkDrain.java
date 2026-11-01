package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.protocol.v1.Event;

import java.util.ArrayList;
import java.util.List;

/** Test access to the consumer side of an {@link EventSink} (package-private in common). */
public final class SinkDrain {

    private SinkDrain() {
    }

    /** Removes and returns every queued event, oldest first. */
    public static List<Event> drain(EventSink sink) {
        List<Event> out = new ArrayList<>();
        for (Event event = sink.poll(); event != null; event = sink.poll()) {
            out.add(event);
        }
        return out;
    }
}
