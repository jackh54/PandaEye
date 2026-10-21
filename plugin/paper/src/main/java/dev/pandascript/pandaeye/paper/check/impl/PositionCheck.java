package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.Move;

import java.util.Locale;
import java.util.Map;

/**
 * protocol.position (CHECKS 1.8): NaN or infinite coordinates or rotation, or a position beyond the world
 * limits ({@code |x|, |z| > 3.0e7}, {@code |y| > 2.0e7}) that no client produces. Only the parts the packet
 * carries are judged. Statistic: the largest coordinate as a fraction of its limit (1000 when not finite).
 * At most one flag per second.
 */
public final class PositionCheck extends Check {

    static final double HORIZONTAL_LIMIT = 3.0e7;
    static final double VERTICAL_LIMIT = 2.0e7;
    private static final double NOT_FINITE = 1_000;

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);

    public PositionCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.MoveInput(Move move)) || !(move.hasPos() || move.hasRot())) {
            return;
        }
        CheckSettings settings = context.settings();
        double horizontal = settings.threshold("horizontal-limit", HORIZONTAL_LIMIT);
        double vertical = settings.threshold("vertical-limit", VERTICAL_LIMIT);
        double worst = 0;
        if (move.hasPos()) {
            worst = Math.max(ratio(move.rawX(), horizontal), Math.max(ratio(move.rawY(), vertical),
                    ratio(move.rawZ(), horizontal)));
        }
        if (move.hasRot() && (!Float.isFinite(move.rawYaw()) || !Float.isFinite(move.rawPitch()))) {
            worst = NOT_FINITE;
        }
        context.evaluated(worst);
        if (worst <= 1 || !interval.admit(context.nowNanos())) {
            return;
        }
        String info = move.hasPos()
                ? String.format(Locale.ROOT, "position %s %s %s", move.rawX(), move.rawY(), move.rawZ())
                : String.format(Locale.ROOT, "rotation %s %s", move.rawYaw(), move.rawPitch());
        context.flag(Violation.of(1.0, 1, info, move.hasPos()
                ? Map.of("x", move.rawX(), "y", move.rawY(), "z", move.rawZ())
                : Map.of("yaw", (double) move.rawYaw(), "pitch", (double) move.rawPitch())));
    }

    private static double ratio(double value, double limit) {
        return Double.isFinite(value) ? Math.abs(value) / limit : NOT_FINITE;
    }
}
