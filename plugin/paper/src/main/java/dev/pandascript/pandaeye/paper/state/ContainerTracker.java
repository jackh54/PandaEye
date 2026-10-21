package dev.pandascript.pandaeye.paper.state;

import java.util.BitSet;
import java.util.HashMap;
import java.util.Map;

/**
 * The open container as the server announced it (OPEN_WINDOW / OPEN_HORSE_WINDOW / CLOSE_WINDOW), plus the
 * ids that were closed or replaced and when, so a click for one of them can be judged once the client has
 * applied the close. Unknown after JOIN_GAME/RESPAWN and after a hot load until the next open or close.
 * Netty thread only.
 */
public final class ContainerTracker {

    private static final int MAX_CLOSED = 64;

    private final BitSet everOpened = new BitSet(256);
    private final Map<Integer, Long> closedAt = new HashMap<>();
    private boolean known;
    private long knownSinceNanos;
    private int openId;
    private String openType = "";
    private long lastActivityNanos;
    private boolean anyActivity;

    void open(int id, String type, long nanos) {
        if (openId != 0 && openId != id) {
            close(openId, nanos);
        }
        openId = id;
        openType = type;
        closedAt.remove(id);
        everOpened.set(id & 0xff);
        becomeKnown(nanos);
        touch(nanos);
    }

    /** A close sent by the server: the client applies it later. */
    void serverClose(int id, long nanos) {
        close(id, nanos);
        if (openId == id || id == 0) {
            openId = 0;
            openType = "";
        }
        becomeKnown(nanos);
        touch(nanos);
    }

    /** A close sent by the client: it has applied it already. */
    void clientClose(int id, long nanos) {
        if (openId == id) {
            openId = 0;
            openType = "";
        }
        if (id != 0) {
            closedAt.put(id, Long.MIN_VALUE);
        }
        becomeKnown(Long.MIN_VALUE);
        touch(nanos);
    }

    void click(long nanos) {
        touch(nanos);
    }

    void unknown() {
        known = false;
        openId = 0;
        openType = "";
        closedAt.clear();
        everOpened.clear();
    }

    public boolean known() {
        return known;
    }

    /**
     * When the state last became known (the open/close that ended an unknown period), {@link Long#MIN_VALUE}
     * when the client itself caused it. Until the client has applied that packet, clicks may still come from a
     * window opened before. Only meaningful while {@link #known()}.
     */
    public long knownSinceNanos() {
        return knownSinceNanos;
    }

    /** The open non-player container id, 0 for none. */
    public int openId() {
        return openId;
    }

    public String openType() {
        return openType;
    }

    public boolean everOpened(int id) {
        return everOpened.get(id & 0xff);
    }

    /** True when {@code id} was closed or replaced and the client has applied that (per {@code clock}). */
    public boolean closedAndApplied(int id, AckClock clock) {
        Long at = closedAt.get(id);
        return at != null && (at == Long.MIN_VALUE || clock.applied(at));
    }

    /** Last open/close/click, for telemetry focus; only meaningful when {@link #anyActivity()}. */
    public long lastActivityNanos() {
        return lastActivityNanos;
    }

    public boolean anyActivity() {
        return anyActivity;
    }

    private void becomeKnown(long nanos) {
        if (!known) {
            known = true;
            knownSinceNanos = nanos;
        }
    }

    private void close(int id, long nanos) {
        if (id == 0) {
            return;
        }
        if (closedAt.size() >= MAX_CLOSED) {
            closedAt.clear();
        }
        closedAt.put(id, nanos);
    }

    private void touch(long nanos) {
        lastActivityNanos = nanos;
        anyActivity = true;
    }
}
