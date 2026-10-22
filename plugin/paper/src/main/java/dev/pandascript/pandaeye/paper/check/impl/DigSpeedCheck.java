package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.check.Confirmation;
import dev.pandascript.pandaeye.paper.check.Confirmer;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.DigProbe;
import dev.pandascript.pandaeye.paper.state.EffectTracker;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.Tracked;

import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * dig.speed (CHECKS 1.6c): a block takes {@code expected = ceil(1 / perTick)} client ticks to break, where
 * {@code perTick} is Paper's {@code Block#getBreakSpeed(Player)}. {@code ticks} = client ticks between START
 * and FINISH; vanilla needs {@code expected - 1} (click) or {@code expected} (held). A FINISH is a candidate
 * when {@code (ticks + margin) * perTick < 0.7}: with the margin of 1 tick this is exactly the break that the
 * vanilla server refuses (progress {@code perTick * (ticks + 1)} below 0.7). The confirmation then asks the
 * server's own ruling: when the block is gone, the server accepted the break (custom tools, plugins that
 * change mining speed, anything the probe cannot see) and the candidate is discarded.
 *
 * <p>The server breaks the block while it handles FINISH, before a confirmer could look at it, so
 * {@code perTick} is taken on the region thread while the server handles the START (a {@link DigProbe}, from
 * BlockDamageEvent). It is made lenient: computed as if on the ground and with the faster of in/out of water.
 * The check skips when the client's view of anything else that affects the speed could differ from the
 * server's: haste, conduit power or mining fatigue, or a mining attribute, changed during the dig, is pending,
 * or does not match the probe.
 */
public final class DigSpeedCheck extends Check {

    static final double AIRBORNE_FACTOR = 5;
    private static final long PROBE_WAIT_NANOS = 1_000_000_000L;
    private static final double ATTRIBUTE_TOLERANCE = 1e-4;

    /** The client's view of everything besides the tool and block that sets the break speed. */
    private record ClientView(int digBoost, int fatigue, double efficiency, double breakSpeed, double submerged,
                              boolean pending) {

        boolean sameAs(ClientView other) {
            return digBoost == other.digBoost && fatigue == other.fatigue && efficiency == other.efficiency
                    && breakSpeed == other.breakSpeed && submerged == other.submerged;
        }

        /** False when the client could dig faster than the probe's state allows. */
        boolean matches(DigProbe probe) {
            return digBoost <= probe.digBoost() && fatigue >= probe.fatigue()
                    && Math.abs(efficiency - probe.miningEfficiency()) <= ATTRIBUTE_TOLERANCE
                    && Math.abs(breakSpeed - probe.blockBreakSpeed()) <= ATTRIBUTE_TOLERANCE
                    && Math.abs(submerged - probe.submergedMiningSpeed()) <= ATTRIBUTE_TOLERANCE;
        }
    }

    private PacketState.DigStart start;
    private ClientView atStart;
    private PacketState.DigStart waitingStart;
    private ClientView waitingView;
    private int waitingTicks;
    private long waitingSince;

    public DigSpeedCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.Dig dig when dig.action() == CheckInput.DigAction.START -> {
                start = context.state().digStart();
                atStart = view(context.state());
            }
            case CheckInput.Dig dig when dig.action() == CheckInput.DigAction.FINISH -> finish(dig.start(), context);
            case CheckInput.Dig dig when dig.action() == CheckInput.DigAction.CANCEL -> start = null;
            case CheckInput.Reset ignored -> {
                start = null;
                waitingStart = null;
            }
            default -> {
            }
        }
        if (waitingStart != null) {
            evaluateWhenProbed(context);
        }
    }

    private void finish(PacketState.DigStart finished, CheckContext context) {
        PacketState state = context.state();
        PacketState.DigStart begun = start;
        start = null;
        if (finished == null || !finished.equals(begun) || !state.client().tickClient() || !state.gameMode().known()
                || state.creativeOrSpectatorLenient() || state.dead()) {
            return;
        }
        ClientView now = view(state);
        if (atStart.pending() || now.pending() || !now.sameAs(atStart)) {
            return;
        }
        waitingStart = finished;
        waitingView = now;
        waitingTicks = state.tick() - finished.tick();
        waitingSince = context.nowNanos();
    }

    /** Evaluates a FINISH once the region thread has handled its START (usually before the FINISH arrives). */
    private void evaluateWhenProbed(CheckContext context) {
        PacketState.DigStart dig = waitingStart;
        DigProbe probe = context.state().signals().digProbe(dig.x(), dig.y(), dig.z(), dig.nanos());
        if (probe == null) {
            if (context.nowNanos() - waitingSince > PROBE_WAIT_NANOS) {
                waitingStart = null;
            }
            return;
        }
        waitingStart = null;
        if (probe.instaBreak() || !waitingView.matches(probe)) {
            return;
        }
        double perTick = lenientPerTick(probe);
        if (!(perTick > 0) || perTick >= 1) {
            return;
        }
        double expected = Math.ceil(1 / perTick);
        int ticks = waitingTicks;
        context.evaluated(1 - (ticks + 1) / expected);
        CheckSettings settings = context.settings();
        if ((ticks + settings.threshold("margin-ticks", 1)) * perTick >= settings.threshold("factor", 0.7)) {
            return;
        }
        context.candidate(Violation.of(Math.min(1, 1 - ticks / expected), 1,
                        String.format(Locale.ROOT, "broke a block in %d ticks, expected %.0f", ticks, expected),
                        Map.of("ticks", (double) ticks, "expected", expected, "per_tick", perTick,
                                "break_speed", (double) probe.breakSpeed())),
                List.of(new ConfirmContext.Position(dig.x() + 0.5, dig.y(), dig.z() + 0.5)), false,
                confirm -> confirm.world().blockPresent(dig.x(), dig.y(), dig.z())
                        ? Confirmation.CONFIRMED
                        : Confirmation.discarded("the server accepted the break"));
    }

    /** The probe's speed as if on the ground, with the faster of in and out of water. NaN when not computable. */
    static double lenientPerTick(DigProbe probe) {
        double perTick = probe.breakSpeed();
        if (!probe.onGround()) {
            perTick *= AIRBORNE_FACTOR;
        }
        double submerged = probe.submergedMiningSpeed();
        if (probe.underWater()) {
            if (!(submerged > 0)) {
                return Double.NaN;
            }
            if (submerged < 1) {
                perTick /= submerged;
            }
        } else if (submerged > 1) {
            perTick *= submerged;
        }
        return perTick;
    }

    private static ClientView view(PacketState state) {
        EffectTracker effects = state.effects();
        AttributeTracker attributes = state.attributes();
        Tracked<Double> efficiency = attributes.get(AttributeTracker.MINING_EFFICIENCY);
        Tracked<Double> breakSpeed = attributes.get(AttributeTracker.BLOCK_BREAK_SPEED);
        Tracked<Double> submerged = attributes.get(AttributeTracker.SUBMERGED_MINING_SPEED);
        boolean pending = changing(effects, EffectTracker.HASTE) || changing(effects, EffectTracker.CONDUIT_POWER)
                || changing(effects, EffectTracker.MINING_FATIGUE) || efficiency.isPending() || breakSpeed.isPending()
                || submerged.isPending() || !efficiency.known() || !breakSpeed.known() || !submerged.known();
        return new ClientView(
                Math.max(effects.maxAmplifier(EffectTracker.HASTE), effects.maxAmplifier(EffectTracker.CONDUIT_POWER)),
                effects.minAmplifier(EffectTracker.MINING_FATIGUE),
                value(efficiency), value(breakSpeed), value(submerged), pending);
    }

    private static boolean changing(EffectTracker effects, String key) {
        return effects.maxAmplifier(key) != effects.minAmplifier(key);
    }

    private static double value(Tracked<Double> tracked) {
        Double latest = tracked.latest();
        return latest == null ? Double.NaN : latest;
    }
}
