package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.SlotTracker;

import java.util.Locale;
import java.util.Map;

/**
 * protocol.slot (CHECKS 1.9), against the mirror of the client's {@code carriedIndex} (0 on JOIN_GAME, changed
 * only by the client's HELD_ITEM_CHANGE; a server SET_HELD_ITEM expects one echo, which is fine). A slot
 * outside 0..8 is variant {@code range} (10 points); selecting the slot that is already selected is variant
 * {@code redundant} (0 points, no VL: logged until shadow data says what it is worth). Statistic: 0 normal,
 * 1 redundant, 2 out of range. At most one flag per second and variant.
 */
public final class SlotCheck extends Check {

    static final String RANGE = "range";
    static final String REDUNDANT = "redundant";

    private final FlagInterval rangeInterval = new FlagInterval(FlagInterval.SECOND);
    private final FlagInterval redundantInterval = new FlagInterval(FlagInterval.SECOND);

    public SlotCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.HeldItem(int slot, SlotTracker.Change change))
                || change == SlotTracker.Change.UNKNOWN) {
            return;
        }
        long now = context.nowNanos();
        switch (change) {
            case OUT_OF_RANGE -> {
                context.evaluated(2);
                if (rangeInterval.admit(now)) {
                    context.flag(Violation.of(1.0, 1, String.format(Locale.ROOT, "held slot %d outside 0..8", slot),
                            Map.of("slot", (double) slot)).withVariant(RANGE));
                }
            }
            case REDUNDANT -> {
                context.evaluated(1);
                if (redundantInterval.admit(now)) {
                    context.flag(Violation.of(1.0, 0, String.format(Locale.ROOT, "held slot %d already selected", slot),
                            Map.of("slot", (double) slot)).withVariant(REDUNDANT));
                }
            }
            default -> context.evaluated(0);
        }
    }
}
