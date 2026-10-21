package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.Map;

/**
 * protocol.abilities (CHECKS 1.13): the client may toggle flying only when the server allows flight. A client
 * PLAYER_ABILITIES with flying = true while allowFlight is false in the applied and every pending state, in
 * survival or adventure (applied and pending), is forged. A permission that ended less than a second ago still
 * counts (it may be applied a frame early). Statistic: 1 for an unpermitted toggle, else 0. At most one flag
 * per second.
 */
public final class AbilitiesCheck extends Check {

    private final FlagInterval interval = new FlagInterval(FlagInterval.SECOND);
    private long permittedNanos;
    private boolean permittedBefore;

    public AbilitiesCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        PacketState state = context.state();
        long now = context.nowNanos();
        boolean known = state.abilities().known() && state.gameMode().known();
        boolean permitted = known && (state.allowFlightLenient() || state.creativeOrSpectatorLenient());
        if (permitted) {
            permittedNanos = now;
            permittedBefore = true;
        }
        if (!(input instanceof CheckInput.ClientAbilities(boolean flying)) || !flying || !known) {
            return;
        }
        context.evaluated(permitted ? 0 : 1);
        if (permitted || (permittedBefore && now - permittedNanos < MoveSupport.HOLD_NANOS)
                || !interval.admit(now)) {
            return;
        }
        context.flag(Violation.of(1.0, 1, "started flying without allowFlight", Map.of("flying", 1.0)));
    }
}
