package dev.pandascript.pandaeye.paper.state;

/** Decides whether a server-side change sent at a given time has been applied by the client. */
@FunctionalInterface
public interface AckClock {

    boolean applied(long sentNanos);
}
