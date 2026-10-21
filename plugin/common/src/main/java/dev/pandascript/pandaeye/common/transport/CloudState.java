package dev.pandascript.pandaeye.common.transport;

/** Connection state of the {@link CloudClient}. */
public enum CloudState {
    /** Not started, or stopped. */
    STOPPED,
    /** Loading identity, seq state and the spool. */
    STARTING,
    /** Opening the WebSocket. */
    CONNECTING,
    /** Hello sent, waiting for Welcome. */
    HANDSHAKING,
    /** Welcome received; streaming. */
    READY,
    /** Disconnected; waiting before the next attempt (degraded mode: events are buffered). */
    WAITING
}
