package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;

import java.util.Map;

/**
 * protocol.selfhit (CHECKS 1.10): INTERACT_ENTITY (any action) or the 26.1+ ATTACK packet aimed at the
 * player's own entity, which the vanilla client excludes from picking. Skipped while the own entity id is
 * unknown. At most one flag per second.
 */
public final class SelfHitCheck extends Check {

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);

    public SelfHitCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.Interact(int target, boolean attack))) {
            return;
        }
        int own = context.state().entityId();
        if (own < 0) {
            return;
        }
        boolean self = target == own;
        context.evaluated(self ? 1 : 0);
        if (self && interval.admit(context.nowNanos())) {
            context.flag(Violation.of(1.0, 1, attack ? "attacked its own entity" : "interacted with its own entity",
                    Map.of("entity_id", (double) target, "attack", attack ? 1.0 : 0.0)));
        }
    }
}
