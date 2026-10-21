package dev.pandascript.pandaeye.paper.stall;

import dev.pandascript.pandaeye.common.util.DaemonThreads;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionSignals;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;

import java.util.ArrayDeque;
import java.util.Collection;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.function.Supplier;

/**
 * Server stall detection (ARCHITECTURE 4.5) on a daemon thread ({@code PandaEye-Stall}, every 50 ms): a stall
 * is in progress when at least 20% of active players (and at least 3) sent nothing for 400 ms, or the
 * global-region heartbeat (a 1-tick repeating task) is more than 400 ms late. Finished stalls are sent as
 * ServerStall events and excuse timer bins.
 */
public final class StallDetector implements SessionSignals.StallClock {

    static final long PERIOD_MILLIS = 50;
    static final long SILENCE_NANOS = 400_000_000L;
    static final long ACTIVE_NANOS = 10_000_000_000L;
    static final double SILENT_SHARE = 0.2;
    static final int MIN_SILENT = 3;
    private static final int HISTORY = 64;

    private record Interval(long start, long end) {
    }

    private final Supplier<Collection<Session>> sessions;
    private final Telemetry telemetry;
    private final DaemonThreads threads = DaemonThreads.named("PandaEye-Stall");
    private final ArrayDeque<Interval> history = new ArrayDeque<>();
    private volatile long lastHeartbeatNanos;
    private volatile long stallStartNanos;
    private volatile boolean inStall;
    private float maxShare;
    private long stalls;
    private ScheduledExecutorService executor;

    public StallDetector(Supplier<Collection<Session>> sessions, Telemetry telemetry) {
        this.sessions = sessions;
        this.telemetry = telemetry;
    }

    public void start() {
        ScheduledThreadPoolExecutor scheduled = new ScheduledThreadPoolExecutor(1, threads);
        scheduled.setExecuteExistingDelayedTasksAfterShutdownPolicy(false);
        executor = scheduled;
        scheduled.scheduleAtFixedRate(this::check, PERIOD_MILLIS, PERIOD_MILLIS, TimeUnit.MILLISECONDS);
    }

    public void stop(long deadlineNanos) {
        if (executor == null) {
            return;
        }
        executor.shutdownNow();
        try {
            executor.awaitTermination(Math.max(0, deadlineNanos - System.nanoTime()), TimeUnit.NANOSECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        threads.joinAll(deadlineNanos);
    }

    /** Global region thread, every tick. */
    public void heartbeat() {
        lastHeartbeatNanos = System.nanoTime();
    }

    public synchronized long stalls() {
        return stalls;
    }

    public boolean inStall() {
        return inStall;
    }

    @Override
    public synchronized boolean overlaps(long fromNanos, long toNanos) {
        if (inStall && toNanos - stallStartNanos >= 0) {
            return true;
        }
        for (Interval interval : history) {
            if (interval.start() - toNanos <= 0 && interval.end() - fromNanos >= 0) {
                return true;
            }
        }
        return false;
    }

    void check() {
        try {
            check(System.nanoTime());
        } catch (Throwable ignored) {
            // the detector must never die; a missed sample is harmless
        }
    }

    synchronized void check(long now) {
        int active = 0;
        int silent = 0;
        for (Session session : sessions.get()) {
            long idle = now - session.lastPacketNanos();
            if (idle < ACTIVE_NANOS) {
                active++;
                if (idle >= SILENCE_NANOS) {
                    silent++;
                }
            }
        }
        float share = active == 0 ? 0 : (float) silent / active;
        long heartbeat = lastHeartbeatNanos;
        boolean stalled = (silent >= MIN_SILENT && share >= SILENT_SHARE)
                || (heartbeat != 0 && now - heartbeat > SILENCE_NANOS);
        if (stalled && !inStall) {
            stallStartNanos = now - SILENCE_NANOS;
            inStall = true;
            maxShare = share;
        } else if (stalled) {
            maxShare = Math.max(maxShare, share);
        } else if (inStall) {
            inStall = false;
            stalls++;
            history.addLast(new Interval(stallStartNanos, now));
            while (history.size() > HISTORY) {
                history.pollFirst();
            }
            telemetry.serverStall(stallStartNanos, now, maxShare);
        }
    }
}
