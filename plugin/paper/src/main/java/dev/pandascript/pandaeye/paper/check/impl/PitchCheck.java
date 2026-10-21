package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.protocol.v1.MoveFlag;

import java.util.Locale;
import java.util.Map;

/**
 * protocol.pitch (CHECKS 1.7): the vanilla client clamps its pitch to [-90, 90], so a rotation outside it is
 * forged, unless the server itself set an out-of-range pitch. At most one flag per second.
 */
public final class PitchCheck extends Check {

    static final double DEFAULT_LIMIT = 90.001;
    private static final long FLAG_INTERVAL_NANOS = 1_000_000_000L;

    private long lastFlagNanos;
    private boolean flaggedBefore;

    public PitchCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.MoveInput(Move move)) || !move.hasRot()) {
            return;
        }
        double pitch = Math.abs(move.rawPitch());
        context.evaluated(pitch);
        if (!(pitch > context.settings().threshold("limit", DEFAULT_LIMIT))
                || move.has(MoveFlag.MOVE_FLAG_SERVER_ROTATION_VALUE)
                || context.state().serverPitchOutOfRange()) {
            return;
        }
        long now = context.nowNanos();
        if (flaggedBefore && now - lastFlagNanos < FLAG_INTERVAL_NANOS) {
            return;
        }
        flaggedBefore = true;
        lastFlagNanos = now;
        context.flag(Violation.of(1.0, 1, String.format(Locale.ROOT, "pitch %.3f", move.rawPitch()),
                Map.of("pitch", (double) move.rawPitch())));
    }
}
