package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Box;
import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.check.Confirmation;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.check.WorldView;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;

import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * movement.step (CHECKS 1.5): from the ground, one tick can rise at most by the step height (stepping onto a
 * block) or by the jump power, {@code dy[n] <= max(stepHeight, jumpStrength + jumpBoost)}. Step height, jump
 * strength and jump boost are the highest applied or pending values, held for a second so a decrease that is
 * applied a frame early cannot make the check stricter. A rise beyond that is a candidate at once; the
 * confirmation requires open air around position n (bounce and piston blocks under n-1 are exempt).
 */
public final class StepCheck extends Check {

    static final double EPSILON = 0.001;
    static final double JUMP_BOOST_PER_LEVEL = 0.1;
    static final double OPEN_AIR = 1.0;
    static final double EXEMPT_INFLATE = 0.3;
    private static final long ENV_WINDOW_NANOS = 1_000_000_000L;

    /** Inputs of one evaluation, captured for the confirmation. */
    record Sample(double dy, double limit, double stepHeight, double jumpPower, double scale) {
    }

    private final MoveSupport.ExemptionGate gate = new MoveSupport.ExemptionGate();
    private final MoveSupport.Held stepHeight = new MoveSupport.Held(true);
    private final MoveSupport.Held jumpPower = new MoveSupport.Held(true);

    public StepCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.MoveInput(Move n) when n.hasPos() -> onMove(n, context);
            case CheckInput.Reset ignored -> gate.exempt(context.nowNanos());
            default -> {
            }
        }
    }

    private void onMove(Move n, CheckContext context) {
        PacketState state = context.state();
        long now = context.nowNanos();
        int boost = state.effects().maxAmplifier(EffectTracker.JUMP_BOOST);
        double step = stepHeight.update(state.attributes().lenientMax(AttributeTracker.STEP_HEIGHT), now);
        double jump = jumpPower.update(state.attributes().lenientMax(AttributeTracker.JUMP_STRENGTH)
                + (boost >= 0 ? JUMP_BOOST_PER_LEVEL * (boost + 1) : 0), now);
        if (!gate.clear(state, now) || !state.moves().consecutivePositions(1)) {
            return;
        }
        Move n1 = state.moves().get(1);
        double scale = state.attributes().lenientMax(AttributeTracker.SCALE);
        if (!n1.onGround() || Double.isNaN(step) || Double.isNaN(jump) || Double.isNaN(scale)) {
            return;
        }
        double limit = Math.max(step, jump) + context.settings().threshold("epsilon", EPSILON);
        double excess = n.dy() - limit;
        context.evaluated(excess);
        if (excess <= 0 || state.signals().envRecent(EnvFlag.ENV_FLAG_ON_SLIME_VALUE, ENV_WINDOW_NANOS, now)) {
            return;
        }
        Sample sample = new Sample(n.dy(), limit, step, jump, scale);
        context.candidate(violation(excess, sample), List.of(MoveSupport.position(n1), MoveSupport.position(n)), true,
                confirm -> confirm(sample, confirm));
    }

    /**
     * The confirmation step: nothing exempt (slime, bed, piston, entity...) around n-1 and n, and open air around
     * position n. A rise of less than 1.0 always has the ground it started from within that box, so in practice
     * only high jumps confirm, never a step-up onto a block (deliberately lenient, CHECKS 1.5).
     */
    static Confirmation confirm(Sample sample, ConfirmContext context) {
        WorldView world = context.world();
        double scale = Math.max(sample.scale(), context.scale());
        List<ConfirmContext.Position> positions = context.positions();
        if (world.exemptNear(MoveSupport.around(positions, scale).expand(EXEMPT_INFLATE))) {
            return Confirmation.discarded("exempt block or entity nearby");
        }
        ConfirmContext.Position n = positions.get(positions.size() - 1);
        if (world.hasCollisions(Box.player(n.x(), n.y(), n.z(), scale).expand(OPEN_AIR))) {
            return Confirmation.discarded("not in open air");
        }
        return Confirmation.CONFIRMED;
    }

    static Violation violation(double excess, Sample sample) {
        return Violation.of(Math.min(1, excess / 0.5), 1,
                String.format(Locale.ROOT, "rose %.3f from the ground > %.3f", sample.dy(), sample.limit()),
                Map.of("excess", excess, "dy", sample.dy(), "limit", sample.limit(), "step_height", sample.stepHeight(),
                        "jump_power", sample.jumpPower()));
    }
}
