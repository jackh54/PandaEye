package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;

import java.util.Locale;
import java.util.Map;

/**
 * protocol.cursor (CHECKS 1.11): the cursor of PLAYER_BLOCK_PLACEMENT is the hit location relative to the block's
 * corner. Vanilla accepts any location within 1.0000001 of the block's centre (handleUseItemOn), so each component
 * lies in [-0.5, 1.5]: shapes taller or wider than a block (fences, walls, beds...) give values above 1. NaN or more
 * than 0.001 outside that range is forged. Building mods encode data in it, so the check is weak (3 points).
 * Statistic: the largest distance outside [-0.5, 1.5] (1000 for NaN). At most one flag per second.
 */
public final class CursorCheck extends Check {

    static final double TOLERANCE = 0.001;
    private static final double NOT_FINITE = 1_000;

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);

    public CursorCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.Place place)) {
            return;
        }
        double outside = Math.max(outside(place.cursorX()), Math.max(outside(place.cursorY()), outside(place.cursorZ())));
        context.evaluated(outside);
        if (outside <= context.settings().threshold("tolerance", TOLERANCE) || !interval.admit(context.nowNanos())) {
            return;
        }
        context.flag(Violation.of(1.0, 1, String.format(Locale.ROOT, "cursor %s %s %s", place.cursorX(),
                        place.cursorY(), place.cursorZ()),
                Map.of("cursor_x", (double) place.cursorX(), "cursor_y", (double) place.cursorY(),
                        "cursor_z", (double) place.cursorZ())));
    }

    private static double outside(float value) {
        if (!Float.isFinite(value)) {
            return NOT_FINITE;
        }
        return Math.max(0, Math.max(-0.5 - value, value - 1.5));
    }
}
