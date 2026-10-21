package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.Locale;
import java.util.Map;

/**
 * protocol.flood (CHECKS 1.12): a tick client sends at most one movement packet per tick, plus one PosRot per
 * teleport it confirmed and one Rot per server PLAYER_ROTATION in that tick. Three or more ticks over that
 * within the last 20 is a violation (the window restarts). The tick right after a reset is not judged (its
 * counters may include packets from before the join). Statistic: excess ticks in the window. At most one flag
 * per second.
 */
public final class FloodCheck extends Check {

    static final int WINDOW_TICKS = 20;
    private static final int WINDOW_MASK = (1 << WINDOW_TICKS) - 1;

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);
    private int window;
    private boolean skipNext = true;

    public FloodCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.TickEndInput(PacketState.TickEnd tick) -> onTick(tick, context);
            case CheckInput.Reset ignored -> {
                window = 0;
                skipNext = true;
            }
            default -> {
            }
        }
    }

    private void onTick(PacketState.TickEnd tick, CheckContext context) {
        if (!context.state().client().tickClient()) {
            return;
        }
        if (skipNext) {
            skipNext = false;
            window = 0;
            return;
        }
        int allowed = 1 + tick.teleportConfirms() + tick.serverRotations();
        boolean excess = tick.flyingPackets() > allowed;
        window = ((window << 1) | (excess ? 1 : 0)) & WINDOW_MASK;
        int excessTicks = Integer.bitCount(window);
        context.evaluated(excessTicks);
        CheckSettings settings = context.settings();
        if (excessTicks < settings.threshold("excess-ticks", 3)) {
            return;
        }
        window = 0;
        if (interval.admit(context.nowNanos())) {
            context.flag(Violation.of(1.0, 1, String.format(Locale.ROOT,
                            "%d of %d ticks with too many movement packets (%d > %d)", excessTicks, WINDOW_TICKS,
                            tick.flyingPackets(), allowed),
                    Map.of("excess_ticks", (double) excessTicks, "packets", (double) tick.flyingPackets(),
                            "allowed", (double) allowed)));
        }
    }
}
