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

import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.AIR_ACCELERATION;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.AIR_FRICTION;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.DEFAULT_FRICTION;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.EPSILON;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.GROUND_ACCELERATION;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.JUMP_CEILING;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.SPRINT_JUMP_BOOST;
import static dev.pandascript.pandaeye.paper.check.impl.SpeedCheck.SPRINT_MULTIPLIER;

/**
 * movement.noslow (CHECKS 1.6b): the movement.speed bound while the player uses an item, with the movement
 * input scaled by vanilla's 0.2 (the sprint-jump term stays). The player counts as using an item while the
 * applied own metadata says "hand active" and no change sent since says otherwise (the server's metadata
 * controls the client's use, item completion is server-side), no RELEASE_USE_ITEM, slot change, drop, swap or
 * inventory click ended it, and at least 3 ticks passed since USE_ITEM. Excess > 0.03 on 5 consecutive packets
 * is a candidate; the confirmation checks that the item really slows (1.21.11 {@code use_effects}) and then
 * recomputes like movement.speed with the real friction.
 */
public final class NoSlowCheck extends Check {

    static final double USE_MULTIPLIER = 0.2;
    private static final double MULTIPLIER_TOLERANCE = 1e-6;
    private static final long ENV_WINDOW_NANOS = 1_000_000_000L;

    /** Inputs of one evaluation, captured for the confirmation recompute. */
    record Sample(double h0, double h1, double dy0, boolean ground1, boolean ground2, double speed, double scale,
                  int useTicks, double minExcess) {

        double bound(double friction2, double friction1, boolean ceiling) {
            double f = ground2 ? friction2 * AIR_FRICTION : AIR_FRICTION;
            double sprint = speed * SPRINT_MULTIPLIER;
            double a = (ground1 ? sprint * GROUND_ACCELERATION / (friction1 * friction1 * friction1) : AIR_ACCELERATION)
                    * USE_MULTIPLIER;
            double jump = ground1 && (dy0 > 0 || ceiling) ? SPRINT_JUMP_BOOST : 0;
            return h1 * f + a + jump + EPSILON;
        }
    }

    private final MoveSupport.ExemptionGate gate = new MoveSupport.ExemptionGate();
    private int streak;
    private int endedUseTick = Integer.MIN_VALUE;
    private boolean useEnded;

    public NoSlowCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.MoveInput(Move n) when n.hasPos() -> onMove(n, context);
            case CheckInput.HeldItem ignored -> endUse(context.state());
            case CheckInput.WindowClick ignored -> endUse(context.state());
            case CheckInput.Dig dig when dig.action() == CheckInput.DigAction.OTHER -> endUse(context.state());
            case CheckInput.Reset ignored -> {
                streak = 0;
                endUse(context.state());
                gate.exempt(context.nowNanos());
            }
            default -> {
            }
        }
    }

    /** The client stops using an item when the item in its hand changes; no packet says so. */
    private void endUse(PacketState state) {
        useEnded = true;
        endedUseTick = state.useItemTick();
    }

    private boolean usingItem(PacketState state) {
        int started = state.useItemTick();
        if (useEnded && endedUseTick == started) {
            return false;
        }
        useEnded = false;
        return started != Integer.MIN_VALUE && state.handActiveSteady() && !state.releasedSinceUseItem()
                && state.tick() - started >= 3;
    }

    private void onMove(Move n, CheckContext context) {
        PacketState state = context.state();
        long now = context.nowNanos();
        if (!usingItem(state) || !gate.clear(state, now) || !state.moves().consecutivePositions(2)) {
            streak = 0;
            return;
        }
        Move n1 = state.moves().get(1);
        Move n2 = state.moves().get(2);
        ExternalSignals signals = state.signals();
        if ((n1.onGround() && n1.has(MoveFlag.MOVE_FLAG_SNEAKING_VALUE))
                || signals.envRecent(EnvFlag.ENV_FLAG_ON_SOUL_SPEED_BLOCK_VALUE, ENV_WINDOW_NANOS, now)) {
            streak = 0;
            return;
        }
        double speed = state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED);
        double scale = state.attributes().lenientMax(AttributeTracker.SCALE);
        if (Double.isNaN(speed) || Double.isNaN(scale)) {
            streak = 0;
            return;
        }
        CheckSettings settings = context.settings();
        Sample sample = new Sample(n.h(), n1.h(), n.dy(), n1.onGround(), n2.onGround(), speed, scale,
                n.tick() - state.useItemTick(), settings.threshold("excess", 0.03));
        double friction2 = signals.envRecent(EnvFlag.ENV_FLAG_ON_ICE_VALUE, ENV_WINDOW_NANOS, now) ? 0.989
                : signals.envRecent(EnvFlag.ENV_FLAG_ON_SLIME_VALUE, ENV_WINDOW_NANOS, now) ? 0.8 : DEFAULT_FRICTION;
        boolean ceiling = signals.envRecent(EnvFlag.ENV_FLAG_LOW_CEILING_VALUE, ENV_WINDOW_NANOS, now);
        double bound = sample.bound(friction2, DEFAULT_FRICTION, ceiling);
        double excess = n.h() - bound;
        context.evaluated(excess);
        streak = excess > sample.minExcess() ? streak + 1 : 0;
        if (streak < settings.threshold("consecutive", 5)) {
            return;
        }
        streak = 0;
        context.candidate(violation(excess, bound, sample), List.of(MoveSupport.position(n2),
                MoveSupport.position(n1), MoveSupport.position(n)), true, confirm -> recompute(sample, confirm));
    }

    /** The confirmation step: the item in use slows movement, then movement.speed's recompute with 0.2 input. */
    static Confirmation recompute(Sample sample, ConfirmContext context) {
        WorldView world = context.world();
        double multiplier = world.usingItemSpeedMultiplier();
        if (!(multiplier <= USE_MULTIPLIER + MULTIPLIER_TOLERANCE)) {
            return Confirmation.discarded(Double.isNaN(multiplier) ? "not using a slowing item"
                    : String.format(Locale.ROOT, "item slows only to %.2f", multiplier));
        }
        List<ConfirmContext.Position> p = context.positions();
        ConfirmContext.Position p2 = p.get(0);
        ConfirmContext.Position p1 = p.get(1);
        if (world.soulSpeedBlockUnder(p2.x(), p2.y(), p2.z()) || world.soulSpeedBlockUnder(p1.x(), p1.y(), p1.z())) {
            return Confirmation.discarded("soul sand or soul soil under the player");
        }
        double scale = Math.max(sample.scale(), context.scale());
        if (world.exemptNear(MoveSupport.around(p, scale).expand(0.3))) {
            return Confirmation.discarded("exempt block or entity nearby");
        }
        double friction2 = world.maxFriction(p2.x(), p2.y(), p2.z());
        double friction1 = world.minFriction(p1.x(), p1.y(), p1.z());
        double head = p1.y() + Box.PLAYER_HEIGHT * scale;
        boolean ceiling = world.hasCollisions(new Box(p1.x() - 0.3, head, p1.z() - 0.3, p1.x() + 0.3,
                head + JUMP_CEILING, p1.z() + 0.3));
        double bound = sample.bound(friction2, friction1, ceiling);
        double excess = sample.h0() - bound;
        if (excess <= sample.minExcess()) {
            return Confirmation.discarded(String.format(Locale.ROOT, "within the bound with real friction (%.3f/%.3f)",
                    friction2, friction1));
        }
        return Confirmation.confirmed(violation(excess, bound, sample));
    }

    static Violation violation(double excess, double bound, Sample sample) {
        return Violation.of(Math.min(1, excess / 0.15), 1,
                String.format(Locale.ROOT, "h %.3f > bound %.3f while using an item", sample.h0(), bound),
                Map.of("excess", excess, "bound", bound, "h", sample.h0(), "h_prev", sample.h1(),
                        "speed_attribute", sample.speed(), "use_ticks", (double) sample.useTicks()));
    }
}
