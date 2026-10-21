package dev.pandascript.pandaeye.paper.session;

import com.github.retrooper.packetevents.netty.channel.ChannelHelper;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.env.EnvSampler;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.packet.ActiveGate;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import dev.pandascript.pandaeye.paper.state.Baseline;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import dev.pandascript.pandaeye.paper.telemetry.WatchRegistry;
import org.bukkit.entity.Player;

import java.util.function.LongConsumer;
import java.util.function.Supplier;

/**
 * Platform side of the session lifecycle: once the Bukkit player is known, one task on the player's entity
 * thread evaluates the bypass permission, takes the hot-load snapshot (hot-loaded sessions), registers the
 * alert recipient and starts the environment sampler. On close: PlayerQuit, task cancellation, cleanup.
 */
public final class SessionLifecycle implements SessionRegistry.Lifecycle {

    private final Schedulers schedulers;
    private final ActiveGate gate;
    private final EnvSampler sampler;
    private final AlertService alerts;
    private final Telemetry telemetry;
    private final EntityIndex entities;
    private final WatchRegistry watches;
    private final Supplier<PluginConfig> config;
    private final RateLimitedLog errors;

    public SessionLifecycle(Schedulers schedulers, ActiveGate gate, EnvSampler sampler, AlertService alerts,
                            Telemetry telemetry, EntityIndex entities, WatchRegistry watches,
                            Supplier<PluginConfig> config, RateLimitedLog errors) {
        this.schedulers = schedulers;
        this.gate = gate;
        this.sampler = sampler;
        this.alerts = alerts;
        this.telemetry = telemetry;
        this.entities = entities;
        this.watches = watches;
        this.config = config;
        this.errors = errors;
    }

    @Override
    public void attached(Session session, Player player) {
        session.player(player);
        schedulers.runOnEntity(player, session.uuid(), task -> seed(session, player), null, 0);
    }

    @Override
    public void closed(Session session, String reason) {
        telemetry.playerQuit(session, reason, System.nanoTime());
        schedulers.cancel(session.uuid());
        alerts.recipients().quit(session.uuid());
        alerts.forget(session.uuid());
        watches.forget(session.uuid());
    }

    /** Runs {@code task} on the session's netty event loop (where its packet state lives), gated. */
    public void onNetty(Session session, LongConsumer task) {
        ChannelHelper.runInEventLoop(session.user().getChannel(), () -> {
            if (!gate.enter()) {
                return;
            }
            try {
                if (!session.closed()) {
                    task.accept(System.nanoTime());
                }
            } catch (Throwable t) {
                errors.warn("netty task", "session task failed", t);
            } finally {
                gate.exit();
            }
        });
    }

    private void seed(Session session, Player player) {
        if (!gate.enter()) {
            return;
        }
        try {
            if (session.closed()) {
                return;
            }
            boolean bypass = player.hasPermission("pandaeye.bypass");
            session.signals().bypass(bypass);
            if (session.hotLoaded()) {
                Baseline baseline = BaselineSnapshot.take(player);
                entities.put(baseline.entityId(), session);
                session.publishBaseline(baseline);
            } else if (bypass) {
                onNetty(session, nanos -> telemetry.playerJoin(session, true, nanos));
            }
            alerts.recipients().joined(player, player.hasPermission("pandaeye.alerts"),
                    config.get().inGameAlertsByDefault());
            schedulers.repeatOnEntity(player, session.uuid(), sampler.task(session, player), null, 1,
                    EnvSampler.PERIOD_TICKS);
        } catch (Throwable t) {
            errors.warn("seed", "could not seed the session of " + session.name(), t);
        } finally {
            gate.exit();
        }
    }
}
