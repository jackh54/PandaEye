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
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * movement.groundspoof (CHECKS 1.6): a client that stands on something cannot keep falling, so two
 * consecutive packets that both claim {@code onGround} while each falls by more than 0.1 are a candidate
 * (NoFall). One candidate per streak; the confirmation looks for a supporting block or entity under both
 * positions.
 */
public final class GroundSpoofCheck extends Check {

    static final double FALL = 0.1;
    static final double SUPPORT_BELOW = 0.6;
    static final double SUPPORT_ABOVE = 0.1;
    static final double ENTITY_RADIUS = 1.5;
    static final double EXEMPT_INFLATE = 0.3;

    /** Inputs of one evaluation, captured for the confirmation. */
    record Sample(double dy, double dyPrev, double scale) {
    }

    private final MoveSupport.ExemptionGate gate = new MoveSupport.ExemptionGate();
    private boolean raised;

    public GroundSpoofCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.MoveInput(Move n) when n.hasPos() -> onMove(n, context);
            case CheckInput.Reset ignored -> {
                raised = false;
                gate.exempt(context.nowNanos());
            }
            default -> {
            }
        }
    }

    private void onMove(Move n, CheckContext context) {
        PacketState state = context.state();
        long now = context.nowNanos();
        if (!gate.clear(state, now) || !state.moves().consecutivePositions(2)) {
            raised = false;
            return;
        }
        Move n1 = state.moves().get(1);
        double scale = state.attributes().lenientMax(AttributeTracker.SCALE);
        if (!n.onGround() || !n1.onGround() || Double.isNaN(scale)) {
            raised = false;
            return;
        }
        double fall = -Math.max(n.dy(), n1.dy());
        context.evaluated(fall);
        if (fall <= context.settings().threshold("fall", FALL)) {
            raised = false;
            return;
        }
        if (raised) {
            return;
        }
        Sample sample = new Sample(n.dy(), n1.dy(), scale);
        raised = context.candidate(violation(sample), List.of(MoveSupport.position(n1), MoveSupport.position(n)), true,
                confirm -> confirm(sample, confirm));
    }

    /** The confirmation step: no block in {@code [y-0.6, y+0.1]} under either position, no entity within 1.5. */
    static Confirmation confirm(Sample sample, ConfirmContext context) {
        WorldView world = context.world();
        double scale = Math.max(sample.scale(), context.scale());
        double half = Box.PLAYER_WIDTH * Math.max(1, scale) / 2;
        Box support = null;
        for (ConfirmContext.Position p : context.positions()) {
            Box under = new Box(p.x() - half, p.y() - SUPPORT_BELOW, p.z() - half, p.x() + half, p.y() + SUPPORT_ABOVE,
                    p.z() + half);
            if (world.hasCollisions(under)) {
                return Confirmation.discarded("supporting block");
            }
            support = support == null ? under : support.union(under);
        }
        if (world.collidableEntityNear(support, ENTITY_RADIUS)) {
            return Confirmation.discarded("entity within 1.5");
        }
        if (world.exemptNear(MoveSupport.around(context.positions(), scale).expand(EXEMPT_INFLATE))) {
            return Confirmation.discarded("exempt block or entity nearby");
        }
        return Confirmation.CONFIRMED;
    }

    static Violation violation(Sample sample) {
        return Violation.of(Math.min(1, Math.abs(sample.dy())), 1,
                String.format(Locale.ROOT, "on ground while falling %.3f, %.3f", sample.dyPrev(), sample.dy()),
                Map.of("dy", sample.dy(), "dy_prev", sample.dyPrev()));
    }
}
