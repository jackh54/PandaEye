package dev.pandascript.pandaeye.paper.runtime;

import com.github.retrooper.packetevents.netty.channel.ChannelHelper;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.check.CheckHandle;
import dev.pandascript.pandaeye.paper.check.CheckStatsWindow;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.log.Json;
import dev.pandascript.pandaeye.paper.log.LogWriter;
import dev.pandascript.pandaeye.paper.packet.ActiveGate;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionLifecycle;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import dev.pandascript.pandaeye.paper.stall.StallDetector;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import org.bukkit.entity.Player;

import java.util.concurrent.TimeUnit;
import java.util.function.Supplier;

/**
 * The plugin's repeating work: the 1-tick global heartbeat for the stall detector, the 30 s session sweep,
 * the 60 s PlayerJoin{update=true} re-announce and the 5-minute CheckStats/peaks flush.
 */
final class PeriodicTasks {

    static final int STATS_WINDOW_SECONDS = 300;
    private static final long UNATTACHED_GRACE_NANOS = 60_000_000_000L;

    private final Schedulers schedulers;
    private final ActiveGate gate;
    private final SessionRegistry sessions;
    private final SessionLifecycle lifecycle;
    private final Telemetry telemetry;
    private final LogWriter logs;
    private final Supplier<PluginConfig> config;
    private final StallDetector stalls;
    private final RateLimitedLog errors;

    PeriodicTasks(Schedulers schedulers, ActiveGate gate, SessionRegistry sessions, SessionLifecycle lifecycle,
                  Telemetry telemetry, LogWriter logs, Supplier<PluginConfig> config, StallDetector stalls,
                  RateLimitedLog errors) {
        this.schedulers = schedulers;
        this.gate = gate;
        this.sessions = sessions;
        this.lifecycle = lifecycle;
        this.telemetry = telemetry;
        this.logs = logs;
        this.config = config;
        this.stalls = stalls;
        this.errors = errors;
    }

    void start() {
        schedulers.repeatGlobal(task -> stalls.heartbeat(), 1, 1);
        schedulers.repeatAsync(task -> gated("sweep", this::sweep), 30, 30, TimeUnit.SECONDS);
        schedulers.repeatAsync(task -> gated("announce", this::announce), 60, 60, TimeUnit.SECONDS);
        schedulers.repeatAsync(task -> gated("stats", this::flushStats), STATS_WINDOW_SECONDS, STATS_WINDOW_SECONDS,
                TimeUnit.SECONDS);
    }

    /** Removes sessions whose channel closed or whose player went offline without a quit event. */
    void sweep() {
        long now = System.nanoTime();
        for (Session session : sessions.all()) {
            Player player = session.player();
            boolean gone = !ChannelHelper.isOpen(session.user().getChannel())
                    || (player != null ? !player.isOnline() : now - session.createdNanos() > UNATTACHED_GRACE_NANOS);
            if (gone) {
                sessions.remove(session, "sweep");
            }
        }
    }

    /** Re-announces every seeded session so a restarted worker recovers session info. */
    void announce() {
        for (Session session : sessions.all()) {
            if (session.publishedSeeded()) {
                lifecycle.onNetty(session, nanos -> telemetry.playerJoin(session, true, nanos));
            }
        }
    }

    void flushStats() {
        long now = System.nanoTime();
        boolean logFile = config.get().alerts().logFile();
        for (Session session : sessions.all()) {
            for (CheckHandle handle : session.checks().handles()) {
                CheckStatsWindow.Snapshot stats = handle.stats().drain();
                if (stats.empty()) {
                    continue;
                }
                String check = handle.descriptor().id();
                telemetry.checkStats(session, check, stats, STATS_WINDOW_SECONDS, now);
                if (logFile) {
                    logs.peak(new Json()
                            .put("time", System.currentTimeMillis())
                            .put("player", session.uuid().toString())
                            .put("name", session.name())
                            .put("check", check)
                            .put("max_stat", stats.maxStat())
                            .put("evaluated", stats.evaluated())
                            .put("candidates", stats.candidates())
                            .put("discarded", stats.discarded())
                            .put("confirmed", stats.confirmed())
                            .put("window_s", STATS_WINDOW_SECONDS));
                }
            }
        }
    }

    private void gated(String site, Runnable work) {
        if (!gate.enter()) {
            return;
        }
        try {
            work.run();
        } catch (Throwable t) {
            errors.warn(site, "periodic task failed", t);
        } finally {
            gate.exit();
        }
    }
}
