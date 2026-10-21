package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckContext;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import dev.pandascript.pandaeye.paper.check.Violation;

import java.util.ArrayDeque;
import java.util.Locale;
import java.util.Map;

/**
 * timer.balance (CHECKS 1.2): a legitimate tick client sends tick k at client time {@code c0 + 50k}, so the
 * receive offset {@code (t - t0) - 50k} never falls below {@code c0 + d_min}: lag raises it, catch-up returns
 * to the floor. A timer makes the floor fall continuously. The offset minimum is taken per 2 s bin; after 60 s
 * of calibration a bin is a "new low" when its minimum is below
 * {@code runMin - 2 - 0.0005 * (t_bin - runMinT)} ms: a 0.5 ms/s clock-drift allowance measured from when the
 * running minimum was set. Violation: at least 8 of the last 10 bins are new lows, the running minimum fell
 * by at least 150 ms over them, and by at least 100 ms without the single largest bin-to-bin drop.
 */
public final class TimerBalanceCheck extends Check {

    static final double TICK_MS = 50;
    static final double BIN_MS = 2_000;

    private record Bin(boolean newLow, double runMinBefore, double runMinAfter) {
    }

    private final ArrayDeque<Bin> window = new ArrayDeque<>();
    private boolean started;
    private double t0;
    private long ticks;
    private long binIndex;
    private long binStartNanos;
    private double binMin;
    private double runMin;
    private double runMinBinEnd;
    private long startNanos;

    public TimerBalanceCheck(CheckDescriptor descriptor) {
        super(descriptor);
    }

    @Override
    public void handle(CheckInput input, CheckContext context) {
        switch (input) {
            case CheckInput.TickEndInput tick -> onTick(tick.tick().nanos(), context);
            case CheckInput.Reset ignored -> started = false;
            default -> {
            }
        }
    }

    private void onTick(long nanos, CheckContext context) {
        if (!context.state().client().tickClient() || !context.state().tickRateNormal()) {
            started = false;
            return;
        }
        double t = nanos / 1e6;
        if (!started) {
            started = true;
            t0 = t;
            ticks = 0;
            startNanos = nanos;
            binIndex = 0;
            binStartNanos = nanos;
            binMin = Double.POSITIVE_INFINITY;
            runMin = Double.POSITIVE_INFINITY;
            runMinBinEnd = 0;
            window.clear();
            return;
        }
        ticks++;
        double offset = (t - t0) - TICK_MS * ticks;
        long bin = (long) Math.floor((nanos - startNanos) / 1e6 / BIN_MS);
        if (bin > binIndex) {
            closeBin(context, nanos);
            binIndex = bin;
            binStartNanos = startNanos + (long) (bin * BIN_MS * 1e6);
            binMin = Double.POSITIVE_INFINITY;
        }
        binMin = Math.min(binMin, offset);
    }

    private void closeBin(CheckContext context, long nowNanos) {
        if (binMin == Double.POSITIVE_INFINITY) {
            return;
        }
        CheckSettings settings = context.settings();
        double binEnd = (binIndex + 1) * BIN_MS;
        double before = runMin;
        if (binEnd <= settings.threshold("calibration-ms", 60_000)) {
            lowerRunMin(binEnd);
            return;
        }
        boolean stalled = context.state().signals().stallOverlaps(binStartNanos, nowNanos);
        double drift = settings.threshold("drift-ms-per-s", 0.5) * (binEnd - runMinBinEnd) / 1_000;
        boolean newLow = !stalled && binMin < runMin - settings.threshold("new-low-margin-ms", 2) - drift;
        lowerRunMin(binEnd);
        window.addLast(new Bin(newLow, before, runMin));
        int size = (int) settings.threshold("window-bins", 10);
        while (window.size() > size) {
            window.pollFirst();
        }
        double fall = window.peekFirst().runMinBefore() - window.peekLast().runMinAfter();
        context.evaluated(fall);
        if (window.size() < size) {
            return;
        }
        int newLows = 0;
        double largestDrop = 0;
        for (Bin b : window) {
            if (b.newLow()) {
                newLows++;
            }
            largestDrop = Math.max(largestDrop, b.runMinBefore() - b.runMinAfter());
        }
        if (newLows >= settings.threshold("new-low-bins", 8)
                && fall >= settings.threshold("fall-ms", 150)
                && fall - largestDrop >= settings.threshold("fall-without-largest-ms", 100)) {
            double seconds = size * BIN_MS / 1_000;
            context.flag(Violation.of(Math.min(1, fall / 300), 1,
                    String.format(Locale.ROOT, "clock gained %.0f ms in %.0f s (%d/%d new lows)", fall, seconds,
                            newLows, size),
                    Map.of("fall_ms", fall, "new_lows", (double) newLows, "largest_drop_ms", largestDrop)));
            window.clear();
        }
    }

    private void lowerRunMin(double binEnd) {
        if (binMin < runMin) {
            runMin = binMin;
            runMinBinEnd = binEnd;
        }
    }
}
