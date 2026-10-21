package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.List;

/** What a {@link Check} can see and do while it handles an input (netty thread). */
public interface CheckContext {

    /** The player's packet state (read-only accessors). */
    PacketState state();

    /** Current settings of this check (thresholds may change on {@code /pandaeye reload}). */
    CheckSettings settings();

    /** Time of the packet being handled ({@link System#nanoTime()} scale). */
    long nowNanos();

    /** The check's current local VL. */
    double vl();

    /** Records one evaluation and its test statistic for CheckStats tuning data. */
    void evaluated(double statistic);

    /** A violation that needs no world confirmation (protocol and timer checks): it counts immediately. */
    void flag(Violation violation);

    /**
     * A violation that must pass the confirmation step one tick later on the entity thread. Returns false when
     * it was dropped (confirmation limits, unknown world); drops are counted.
     */
    boolean candidate(Violation violation, List<ConfirmContext.Position> positions, boolean movement, Confirmer confirmer);
}
