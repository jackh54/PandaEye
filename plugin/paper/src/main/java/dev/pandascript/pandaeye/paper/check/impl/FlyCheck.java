package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Box;
import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.check.Confirmation;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.check.WorldView;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * movement.fly (CHECKS 1.4): over consecutive airborne packets n-2, n-1, n the vertical displacement follows
 * vanilla gravity and drag, {@code dy[n] = (dy[n-1] - g) * 0.98} (motion below 0.003 is zeroed). A client
 * without the chunk below falls at exactly -0.098 per tick. Rising or falling slower than that by more than
 * 0.03 on two consecutive packets (or by 0.5 once) is a candidate; the confirmation requires open air.
 * Gravity is the lowest applied or pending value, held for a second: a lower gravity makes the client fall
 * slower, so the lowest is the lenient one.
 */
public final class FlyCheck extends Check {

    static final double DRAG = 0.98;
    static final double MIN_MOTION = 0.003;
    static final double UNLOADED_CHUNK_DY = -0.098;
    static final double UNLOADED_CHUNK_TOLERANCE = 0.001;
    static final double OPEN_AIR = 1.0;
    static final double EXEMPT_INFLATE = 0.3;

    /** Inputs of one evaluation, captured for the confirmation. */
    record Sample(double dy, double dyPrev, double expected, double gravity, double scale) {
    }

    private final MoveSupport.ExemptionGate gate = new MoveSupport.ExemptionGate();
    private final MoveSupport.Held gravity = new MoveSupport.Held(false);
    private int streak;

    public FlyCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.MoveInput(Move n) when n.hasPos() -> onMove(n, context);
            case CheckInput.Reset ignored -> {
                streak = 0;
                gate.exempt(context.nowNanos());
            }
            default -> {
            }
        }
    }

    private void onMove(Move n, CheckContext context) {
        PacketState state = context.state();
        long now = context.nowNanos();
        double g = gravity.update(state.attributes().get(AttributeTracker.GRAVITY).min(Double::doubleValue), now);
        if (!gate.clear(state, now) || !state.moves().consecutivePositions(2)) {
            streak = 0;
            return;
        }
        Move n1 = state.moves().get(1);
        Move n2 = state.moves().get(2);
        double scale = state.attributes().lenientMax(AttributeTracker.SCALE);
        if (n.onGround() || n1.onGround() || n2.onGround() || Double.isNaN(g) || Double.isNaN(scale)) {
            streak = 0;
            return;
        }
        if (Math.abs(n.dy() - UNLOADED_CHUNK_DY) < UNLOADED_CHUNK_TOLERANCE) {
            streak = 0;
            return;
        }
        double expected = (n1.dy() - g) * DRAG;
        if (Math.abs(expected) < MIN_MOTION) {
            expected = 0;
        }
        double diff = n.dy() - expected;
        context.evaluated(diff);
        CheckSettings settings = context.settings();
        streak = diff > settings.threshold("diff", 0.03) ? streak + 1 : 0;
        if (streak < settings.threshold("consecutive", 2) && diff <= settings.threshold("single-diff", 0.5)) {
            return;
        }
        streak = 0;
        Sample sample = new Sample(n.dy(), n1.dy(), expected, g, scale);
        context.candidate(violation(diff, sample), List.of(MoveSupport.position(n2), MoveSupport.position(n1),
                MoveSupport.position(n)), true, confirm -> confirm(sample, confirm));
    }

    /** The confirmation step: open air around all three positions and nothing exempt nearby. */
    static Confirmation confirm(Sample sample, ConfirmContext context) {
        WorldView world = context.world();
        Box around = MoveSupport.around(context.positions(), Math.max(sample.scale(), context.scale()));
        if (world.hasCollisions(around.expand(OPEN_AIR))) {
            return Confirmation.discarded("not in open air");
        }
        if (world.exemptNear(around.expand(EXEMPT_INFLATE))) {
            return Confirmation.discarded("exempt block or entity nearby");
        }
        return Confirmation.CONFIRMED;
    }

    static Violation violation(double diff, Sample sample) {
        return Violation.of(Math.min(1, diff / 0.5), 1,
                String.format(Locale.ROOT, "dy %.4f, expected %.4f", sample.dy(), sample.expected()),
                Map.of("diff", diff, "dy", sample.dy(), "dy_prev", sample.dyPrev(), "expected", sample.expected(),
                        "gravity", sample.gravity()));
    }
}
