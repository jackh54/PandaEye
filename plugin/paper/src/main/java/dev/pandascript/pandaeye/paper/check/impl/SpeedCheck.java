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
import dev.pandascript.pandaeye.paper.state.ExternalSignals;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import dev.pandascript.pandaeye.protocol.v1.MoveFlag;

import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * movement.speed (CHECKS 1.3): over consecutive position packets n-2, n-1, n the horizontal displacement is
 * bounded by vanilla's ground/air movement: {@code h[n] <= h[n-1] * f + a + jump + 0.01}, with friction
 * {@code f} from the block under n-2 and acceleration {@code a} from the block under n-1. The netty side uses
 * the default friction (0.6, or the ice/slime value when the environment sampler saw them in the last
 * second); the confirmation recomputes with the maximum friction under the footprint at n-2 and the minimum
 * under n-1, which are supersets of what vanilla's single supporting block can produce.
 */
public final class SpeedCheck extends Check {

    static final double DEFAULT_FRICTION = 0.6;
    static final double AIR_FRICTION = 0.91;
    static final double AIR_ACCELERATION = 0.026;
    static final double GROUND_ACCELERATION = 0.21600002;
    static final double SPRINT_MULTIPLIER = 1.3;
    static final double SPRINT_JUMP_BOOST = 0.2;
    static final double EPSILON = 0.01;
    static final double JUMP_CEILING = 0.42;
    private static final long ENV_WINDOW_NANOS = 1_000_000_000L;

    /** Inputs of one evaluation, captured for the confirmation recompute. */
    record Sample(double h0, double h1, double dy0, boolean ground1, boolean ground2, double speed, double scale) {

        double bound(double friction2, double friction1, boolean ceiling) {
            double f = ground2 ? friction2 * AIR_FRICTION : AIR_FRICTION;
            double sprint = speed * SPRINT_MULTIPLIER;
            double a = ground1 ? sprint * GROUND_ACCELERATION / (friction1 * friction1 * friction1) : AIR_ACCELERATION;
            double jump = ground1 && (dy0 > 0 || ceiling) ? SPRINT_JUMP_BOOST : 0;
            return h1 * f + a + jump + EPSILON;
        }
    }

    private double buffer;

    public SpeedCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.MoveInput(Move n)) || !n.hasPos()) {
            return;
        }
        PacketState state = context.state();
        long now = context.nowNanos();
        if (state.movementExemption(now) != null || !state.moves().consecutivePositions(2)) {
            return;
        }
        Move n1 = state.moves().get(1);
        Move n2 = state.moves().get(2);
        ExternalSignals signals = state.signals();
        if ((n1.onGround() && n1.has(MoveFlag.MOVE_FLAG_SNEAKING_VALUE))
                || signals.envRecent(EnvFlag.ENV_FLAG_ON_SOUL_SPEED_BLOCK_VALUE, ENV_WINDOW_NANOS, now)) {
            return;
        }
        double speed = state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED);
        double scale = state.attributes().lenientMax(AttributeTracker.SCALE);
        if (Double.isNaN(speed) || Double.isNaN(scale)) {
            return;
        }
        Sample sample = new Sample(n.h(), n1.h(), n.dy(), n1.onGround(), n2.onGround(), speed, scale);
        double friction2 = signals.envRecent(EnvFlag.ENV_FLAG_ON_ICE_VALUE, ENV_WINDOW_NANOS, now) ? 0.989
                : signals.envRecent(EnvFlag.ENV_FLAG_ON_SLIME_VALUE, ENV_WINDOW_NANOS, now) ? 0.8 : DEFAULT_FRICTION;
        boolean ceiling = signals.envRecent(EnvFlag.ENV_FLAG_LOW_CEILING_VALUE, ENV_WINDOW_NANOS, now);
        double bound = sample.bound(friction2, DEFAULT_FRICTION, ceiling);
        double excess = n.h() - bound;
        context.evaluated(excess);
        CheckSettings settings = context.settings();
        if (excess > 0) {
            buffer += 1;
        } else {
            buffer = Math.max(0, buffer - settings.threshold("buffer-decay", 0.1));
        }
        if (buffer < settings.threshold("buffer", 3) && excess <= settings.threshold("single-excess", 0.5)) {
            return;
        }
        buffer = 0;
        context.candidate(violation(excess, bound, sample), List.of(position(n2), position(n1), position(n)), true,
                confirm -> recompute(sample, confirm));
    }

    /** The confirmation step: real friction under the footprints, ceiling, soul speed and exempt surroundings. */
    static Confirmation recompute(Sample sample, ConfirmContext context) {
        List<ConfirmContext.Position> p = context.positions();
        ConfirmContext.Position p2 = p.get(0);
        ConfirmContext.Position p1 = p.get(1);
        ConfirmContext.Position p0 = p.get(2);
        WorldView world = context.world();
        if (world.soulSpeedBlockUnder(p2.x(), p2.y(), p2.z()) || world.soulSpeedBlockUnder(p1.x(), p1.y(), p1.z())) {
            return Confirmation.discarded("soul sand or soul soil under the player");
        }
        Box around = box(p2, sample.scale()).union(box(p1, sample.scale())).union(box(p0, sample.scale())).expand(0.3);
        if (world.exemptNear(around)) {
            return Confirmation.discarded("exempt block or entity nearby");
        }
        double friction2 = world.maxFriction(p2.x(), p2.y(), p2.z());
        double friction1 = world.minFriction(p1.x(), p1.y(), p1.z());
        double head = p1.y() + Box.PLAYER_HEIGHT * sample.scale();
        boolean ceiling = world.hasCollisions(new Box(p1.x() - 0.3, head, p1.z() - 0.3, p1.x() + 0.3,
                head + JUMP_CEILING, p1.z() + 0.3));
        double bound = sample.bound(friction2, friction1, ceiling);
        double excess = sample.h0() - bound;
        if (excess <= 0) {
            return Confirmation.discarded(String.format(Locale.ROOT, "within the bound with real friction (%.3f/%.3f)",
                    friction2, friction1));
        }
        return Confirmation.confirmed(violation(excess, bound, sample));
    }

    static Violation violation(double excess, double bound, Sample sample) {
        return Violation.of(Math.min(1, excess / 0.3), excess > 0.3 ? 2 : 1,
                String.format(Locale.ROOT, "h %.3f > bound %.3f", sample.h0(), bound),
                Map.of("excess", excess, "bound", bound, "h", sample.h0(), "h_prev", sample.h1(),
                        "speed_attribute", sample.speed()));
    }

    private static ConfirmContext.Position position(Move move) {
        return new ConfirmContext.Position(move.x(), move.y(), move.z());
    }

    private static Box box(ConfirmContext.Position p, double scale) {
        return Box.player(p.x(), p.y(), p.z(), scale);
    }
}
