package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Box;
import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.List;

/** Helpers shared by the local movement checks (netty side and confirmers). */
final class MoveSupport {

    /** Grace after any common exemption ends: a change can count as applied up to one client frame early. */
    static final long EXEMPTION_GRACE_NANOS = 250_000_000L;
    /** How long a lenient value is kept after the tracker stopped reporting it. */
    static final long HOLD_NANOS = 1_000_000_000L;

    private MoveSupport() {
    }

    static ConfirmContext.Position position(Move move) {
        return new ConfirmContext.Position(move.x(), move.y(), move.z());
    }

    /** The union of the player boxes at the given positions. */
    static Box around(List<ConfirmContext.Position> positions, double scale) {
        Box box = null;
        for (ConfirmContext.Position p : positions) {
            Box at = Box.player(p.x(), p.y(), p.z(), scale);
            box = box == null ? at : box.union(at);
        }
        return box;
    }

    /**
     * The common exemption of CHECKS 1.1 plus a short grace after it ends. A client that runs several ticks per
     * frame (low FPS) sends the movement packets of a whole frame before it handles the packets that arrived
     * during it, so the first packets after a promotion may still carry the old state.
     */
    static final class ExemptionGate {

        private long lastExemptNanos;
        private boolean exemptBefore;

        /** True when the movement checks may run for the newest packet. */
        boolean clear(PacketState state, long nowNanos) {
            if (state.movementExemption(nowNanos) != null) {
                exempt(nowNanos);
                return false;
            }
            return !exemptBefore || nowNanos - lastExemptNanos >= EXEMPTION_GRACE_NANOS;
        }

        void exempt(long nowNanos) {
            lastExemptNanos = nowNanos;
            exemptBefore = true;
        }
    }

    /** Keeps the most lenient value seen within {@link #HOLD_NANOS} (the highest, or the lowest). */
    static final class Held {

        private final boolean highest;
        private double value = Double.NaN;
        private long since;

        Held(boolean highest) {
            this.highest = highest;
        }

        /** Records the current value and returns the most lenient of it and the held one; NaN stays NaN. */
        double update(double current, long nowNanos) {
            if (Double.isNaN(current)) {
                return current;
            }
            boolean moreLenient = highest ? current >= value : current <= value;
            if (Double.isNaN(value) || moreLenient || nowNanos - since > HOLD_NANOS) {
                value = current;
                since = nowNanos;
            }
            return highest ? Math.max(current, value) : Math.min(current, value);
        }
    }
}
