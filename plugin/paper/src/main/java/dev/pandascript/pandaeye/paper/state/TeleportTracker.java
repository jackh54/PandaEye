package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayDeque;

/**
 * Pending server teleports (CHECKS 1.1): a FIFO of PLAYER_POSITION_AND_LOOK packets, popped by
 * TELEPORT_CONFIRM. Movement checks skip while it is non-empty and for 3 position packets after it empties.
 * Netty thread only.
 */
public final class TeleportTracker {

    static final int GRACE_POSITIONS = 3;
    private static final int MAX_PENDING = 64;

    public record Pending(int id, double x, double y, double z, float yaw, float pitch, long sentNanos) {
    }

    private final ArrayDeque<Pending> fifo = new ArrayDeque<>();
    private int positionsSinceEmpty = GRACE_POSITIONS;
    private Pending confirmed;

    void sent(Pending teleport) {
        if (fifo.size() == MAX_PENDING) {
            fifo.pollFirst();
        }
        fifo.addLast(teleport);
        positionsSinceEmpty = 0;
    }

    /** Pops every teleport up to and including {@code id}; the matched one awaits its PosRot. */
    Pending confirm(int id) {
        Pending match = null;
        for (Pending pending : fifo) {
            if (pending.id() == id) {
                match = pending;
                break;
            }
        }
        if (match == null) {
            return null;
        }
        while (fifo.pollFirst() != match) {
            // drop teleports the client skipped
        }
        confirmed = match;
        positionsSinceEmpty = 0;
        return match;
    }

    /** The confirmed teleport whose PosRot packet has not arrived yet (it becomes the new baseline). */
    Pending takeConfirmed() {
        Pending pending = confirmed;
        confirmed = null;
        return pending;
    }

    void onPositionPacket() {
        if (fifo.isEmpty() && positionsSinceEmpty < GRACE_POSITIONS) {
            positionsSinceEmpty++;
        }
    }

    void clear() {
        fifo.clear();
        confirmed = null;
        positionsSinceEmpty = 0;
    }

    /** True while a teleport is unconfirmed or fewer than 3 position packets followed the last one. */
    public boolean pending() {
        return !fifo.isEmpty() || confirmed != null || positionsSinceEmpty < GRACE_POSITIONS;
    }

    public int unconfirmed() {
        return fifo.size();
    }
}
