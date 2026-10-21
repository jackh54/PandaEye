package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.AckClock;
import dev.pandascript.pandaeye.paper.state.AckTracker;
import dev.pandascript.pandaeye.paper.state.ContainerTracker;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.Locale;
import java.util.Map;

/**
 * inventory.closed (CHECKS 1.14): a CLICK_WINDOW for a container id > 0 that was never opened in this session,
 * or that was closed or replaced (an OPEN_WINDOW / OPEN_HORSE_WINDOW over an open one) and the client has
 * applied that already (passive acknowledgement only: never stricter on time alone). Container state is unknown
 * after a hot load, JOIN_GAME and RESPAWN until the next open or close, and "never opened" is only judged once
 * the client has applied that open or close (an older window may still be on its screen until then).
 * Statistic: 1 for an invalid click, else 0. At most one flag per second.
 */
public final class InventoryClosedCheck extends Check {

    /** Applied once a PONG acknowledged it; a missing acknowledgement never counts as applied. */
    private static final class CloseClock implements AckClock {
        AckTracker acks;

        @Override
        public boolean applied(long sentNanos) {
            return acks.hasAck() && acks.ackedUpTo() - sentNanos >= 0;
        }
    }

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);
    private final CloseClock clock = new CloseClock();

    public InventoryClosedCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        if (!(input instanceof CheckInput.WindowClick click) || click.windowId() <= 0) {
            return;
        }
        PacketState state = context.state();
        ContainerTracker containers = state.containers();
        if (!containers.known()) {
            return;
        }
        long now = context.nowNanos();
        clock.acks = state.acks();
        int id = click.windowId();
        long knownSince = containers.knownSinceNanos();
        boolean neverOpened = !containers.everOpened(id) && (knownSince == Long.MIN_VALUE || clock.applied(knownSince));
        boolean closed = containers.closedAndApplied(id, clock);
        boolean invalid = id != containers.openId() && (neverOpened || closed);
        context.evaluated(invalid ? 1 : 0);
        if (!invalid || !interval.admit(now)) {
            return;
        }
        context.flag(Violation.of(1.0, 1, String.format(Locale.ROOT, "clicked %s container %d",
                        neverOpened ? "never opened" : "closed", id),
                Map.of("window_id", (double) id, "open_id", (double) containers.openId(),
                        "never_opened", neverOpened ? 1.0 : 0.0)));
    }
}
