package dev.pandascript.pandaeye.paper.runtime;

import com.github.retrooper.packetevents.PacketEvents;
import com.github.retrooper.packetevents.event.PacketListenerCommon;
import com.github.retrooper.packetevents.event.PacketListenerPriority;
import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.CloudClient;
import dev.pandascript.pandaeye.common.transport.CloudConfig;
import dev.pandascript.pandaeye.common.transport.EndpointPolicy;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.Fingerprint;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.check.CheckRegistry;
import dev.pandascript.pandaeye.paper.cloud.CloudBridge;
import dev.pandascript.pandaeye.paper.command.PandaEyeCommand;
import dev.pandascript.pandaeye.paper.config.ConfigLoader;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.enforce.LiveEnforcer;
import dev.pandascript.pandaeye.paper.enforce.ShadowEnforcer;
import dev.pandascript.pandaeye.paper.env.EnvSampler;
import dev.pandascript.pandaeye.paper.env.ExtendedReach;
import dev.pandascript.pandaeye.paper.flag.ConfirmationScheduler;
import dev.pandascript.pandaeye.paper.flag.FlagPipeline;
import dev.pandascript.pandaeye.paper.flag.SetbackService;
import dev.pandascript.pandaeye.paper.listener.BukkitListeners;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.log.LogWriter;
import dev.pandascript.pandaeye.paper.packet.ActiveGate;
import dev.pandascript.pandaeye.paper.packet.ClientInfos;
import dev.pandascript.pandaeye.paper.packet.PacketCost;
import dev.pandascript.pandaeye.paper.packet.PandaEyePacketListener;
import dev.pandascript.pandaeye.paper.platform.BukkitLog;
import dev.pandascript.pandaeye.paper.platform.ClassPreloader;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import dev.pandascript.pandaeye.paper.platform.ServerPlatform;
import dev.pandascript.pandaeye.paper.session.BedrockDetector;
import dev.pandascript.pandaeye.paper.session.EntityIndex;
import dev.pandascript.pandaeye.paper.session.ProvisionalValidator;
import dev.pandascript.pandaeye.paper.session.SessionFactory;
import dev.pandascript.pandaeye.paper.session.SessionHooks;
import dev.pandascript.pandaeye.paper.session.SessionLifecycle;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import dev.pandascript.pandaeye.paper.stall.StallDetector;
import dev.pandascript.pandaeye.paper.state.CompositeEvents;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import dev.pandascript.pandaeye.paper.telemetry.WatchRegistry;
import dev.pandascript.pandaeye.paper.verdict.MitigationController;
import dev.pandascript.pandaeye.paper.verdict.PlayerLookup;
import dev.pandascript.pandaeye.paper.verdict.VerdictHandler;
import org.bukkit.Bukkit;
import org.bukkit.command.PluginCommand;
import org.bukkit.entity.Player;
import org.bukkit.event.HandlerList;
import org.bukkit.plugin.java.JavaPlugin;

import java.nio.file.Path;
import java.time.Duration;
import java.time.ZoneId;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.TimeUnit;

/**
 * Everything the plugin runs, wired on enable and torn down within 300 ms on disable (ARCHITECTURE 4.1). Safe
 * to enable on a running server (hot load): sessions are created for online players and stay unseeded until
 * their snapshot is taken on their entity thread.
 */
public final class Runtime {

    private static final long DISABLE_BUDGET_NANOS = TimeUnit.MILLISECONDS.toNanos(300);
    private static final long IN_FLIGHT_WAIT_NANOS = TimeUnit.MILLISECONDS.toNanos(100);
    private static final long LOG_STOP_RESERVE_NANOS = TimeUnit.MILLISECONDS.toNanos(30);

    private final JavaPlugin plugin;
    private final ActiveGate gate = new ActiveGate();
    private final PacketCost cost = new PacketCost();
    private volatile PluginConfig config;
    private Log log;
    private RateLimitedLog errors;
    private Schedulers schedulers;
    private CloudClient cloud;
    private LogWriter logs;
    private StallDetector stalls;
    private volatile SessionRegistry sessions;
    private Enforcement enforcement;
    private FlagPipeline flags;
    private ConfirmationScheduler confirmations;
    private EnvSampler sampler;
    private WatchRegistry watches;
    private AlertService alerts;
    private SessionLifecycle lifecycle;
    private MitigationController mitigations;
    private VerdictHandler verdicts;
    private PacketListenerCommon packetListener;
    private String cloudDetail = "disabled";

    public Runtime(JavaPlugin plugin) {
        this.plugin = plugin;
    }

    public void enable() {
        long started = System.nanoTime();
        log = new BukkitLog(plugin.getLogger(), () -> config != null && config.debug());
        config = loadConfig();
        errors = new RateLimitedLog(log, TimeUnit.SECONDS.toNanos(10));
        ClassPreloader.Result preloaded = ClassPreloader.preload(Runtime.class);
        EventClock clock = EventClock.system();
        Path dataDir = plugin.getDataFolder().toPath();
        CloudListenerRelay relay = new CloudListenerRelay();
        cloud = createCloud(clock, dataDir, relay);

        schedulers = new Schedulers(plugin);
        EntityIndex entities = new EntityIndex();
        Telemetry telemetry = new Telemetry(cloud == null ? null : cloud.sink(), clock, this::config, entities);
        watches = new WatchRegistry();
        alerts = new AlertService(plugin.getLogger(), schedulers, this::config);
        logs = new LogWriter(dataDir.resolve("logs"), log, ZoneId.systemDefault());
        enforcement = new Enforcement(() -> !config.shadow(), this::remoteSettings, new ShadowEnforcer(),
                new LiveEnforcer(plugin));
        flags = new FlagPipeline(telemetry, logs, alerts, new SetbackService(enforcement, this::config), this::config);
        confirmations = new ConfirmationScheduler(schedulers, gate::active, errors);
        ProvisionalValidator provisional = new ProvisionalValidator(schedulers, gate::active);
        sampler = new EnvSampler(telemetry, ExtendedReach.detect(), gate::active);
        stalls = new StallDetector(() -> sessions == null ? List.of() : sessions.all(), telemetry);
        BedrockDetector bedrock = BedrockDetector.detect(Runtime.class.getClassLoader());
        SessionFactory factory = new SessionFactory(clock, this::config, stalls, bedrock::isBedrock,
                CheckRegistry.defaults(), flags::forSession, confirmations::forSession,
                session -> new CompositeEvents(List.of(telemetry.forSession(session),
                        new SessionHooks(session, provisional))),
                (site, error) -> errors.warn(site, "check failed", error));
        lifecycle = new SessionLifecycle(schedulers, gate, sampler, alerts, telemetry, entities,
                watches, this::config, errors);
        sessions = new SessionRegistry(entities, factory, lifecycle);
        mitigations = new MitigationController(enforcement, this::config, sessions::get, sessions::all,
                schedulers::runGlobal, gate::active, log, errors);
        CloudClient client = cloud;
        verdicts = new VerdictHandler(this::config, enforcement, PlayerLookup.sessions(() -> sessions), mitigations,
                alerts, logs, log, dataDir, client == null ? (id, executed, note) -> { } : client::ackVerdict);
        if (cloud != null) {
            relay.target(new CloudBridge(sessions, watches, alerts, verdicts));
        }

        gate.open();
        packetListener = PacketEvents.getAPI().getEventManager().registerListener(
                new PandaEyePacketListener(gate, sessions, this::config,
                        (session, nanos) -> telemetry.playerJoin(session, true, nanos), cost, errors),
                PacketListenerPriority.MONITOR);
        Bukkit.getPluginManager().registerEvents(new BukkitListeners(gate, sessions, errors), plugin);
        PluginCommand command = plugin.getCommand("pandaeye");
        if (command != null) {
            PandaEyeCommand executor = new PandaEyeCommand(this);
            command.setExecutor(executor);
            command.setTabCompleter(executor);
        }
        logs.start();
        stalls.start();
        new PeriodicTasks(schedulers, gate, sessions, lifecycle, telemetry, logs, this::config, stalls, errors).start();
        if (cloud != null) {
            cloud.start();
        }
        mitigations.refresh();
        schedulers.runGlobal(() -> {
            if (gate.active()) {
                verdicts.checkCommands(Bukkit.getCommandMap()::getCommand);
            }
        });
        int hotLoaded = hotLoad(System.nanoTime());
        log.info(String.format(Locale.ROOT,
                "PandaEye %s enabled in %d ms: mode %s, %s, cloud %s, %d online players hot-loaded, "
                        + "%d classes preloaded%s, Floodgate %s",
                plugin.getPluginMeta().getVersion(), TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - started),
                config.mode().name().toLowerCase(Locale.ROOT), ServerPlatform.platform().name(), cloudDetail,
                hotLoaded, preloaded.loaded(), preloaded.failed() > 0 ? " (" + preloaded.failed() + " skipped)" : "",
                bedrock.floodgate() ? "found" : "not found"));
        if (config.shadow()) {
            log.info("shadow mode: PandaEye sends, cancels and modifies nothing and never punishes");
        }
    }

    /** Bounded teardown (ARCHITECTURE 4.1): at most 300 ms, never blocks on the network. */
    public void disable() {
        long deadline = System.nanoTime() + DISABLE_BUDGET_NANOS;
        if (packetListener != null) {
            try {
                PacketEvents.getAPI().getEventManager().unregisterListener(packetListener);
            } catch (Throwable t) {
                plugin.getLogger().warning("could not unregister the PacketEvents listener: " + t);
            }
            packetListener = null;
        }
        boolean drained = gate.close(IN_FLIGHT_WAIT_NANOS);
        if (mitigations != null) {
            mitigations.close();
        }
        HandlerList.unregisterAll(plugin);
        if (schedulers != null) {
            schedulers.cancelAll();
        }
        if (stalls != null) {
            stalls.stop(Math.min(deadline, System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(20)));
        }
        if (cloud != null) {
            long budget = Math.max(0, deadline - System.nanoTime() - LOG_STOP_RESERVE_NANOS);
            cloud.stop(Duration.ofNanos(budget));
        }
        if (logs != null) {
            logs.stop(deadline);
        }
        if (sessions != null) {
            sessions.clear();
        }
        if (alerts != null) {
            alerts.recipients().clear();
        }
        if (!drained) {
            plugin.getLogger().warning("some packet callbacks were still running after 100 ms");
        }
    }

    // ---------------------------------------------------------------------------------------------
    // Accessors for the command
    // ---------------------------------------------------------------------------------------------

    public PluginConfig config() {
        return config;
    }

    public RemoteSettings remoteSettings() {
        return cloud == null ? RemoteSettings.UNKNOWN : cloud.remoteSettings();
    }

    public CloudClient cloud() {
        return cloud;
    }

    public String cloudDetail() {
        return cloudDetail;
    }

    public SessionRegistry sessions() {
        return sessions;
    }

    public Enforcement enforcement() {
        return enforcement;
    }

    public PacketCost cost() {
        return cost;
    }

    public ConfirmationScheduler confirmations() {
        return confirmations;
    }

    public FlagPipeline flags() {
        return flags;
    }

    public LogWriter logs() {
        return logs;
    }

    public StallDetector stalls() {
        return stalls;
    }

    public EnvSampler sampler() {
        return sampler;
    }

    public WatchRegistry watches() {
        return watches;
    }

    public AlertService alerts() {
        return alerts;
    }

    public Schedulers schedulers() {
        return schedulers;
    }

    public SessionLifecycle lifecycle() {
        return lifecycle;
    }

    public VerdictHandler verdicts() {
        return verdicts;
    }

    public RateLimitedLog errors() {
        return errors;
    }

    /** Reloads check thresholds and alert settings only (mode, cloud and the rest need a restart). */
    public List<String> reload() {
        plugin.reloadConfig();
        ConfigLoader loader = new ConfigLoader();
        PluginConfig fresh = loader.load(plugin.getConfig());
        PluginConfig current = config;
        config = new PluginConfig(current.mode(), current.serverName(), current.networkId(), current.cloud(),
                current.telemetry(), fresh.checks(), fresh.alerts(), current.exempt(), current.proxy(),
                current.setbacks(), current.punishments(), current.mitigations(), current.debug());
        return loader.warnings();
    }

    // ---------------------------------------------------------------------------------------------

    private PluginConfig loadConfig() {
        plugin.saveDefaultConfig();
        plugin.reloadConfig();
        ConfigLoader loader = new ConfigLoader();
        PluginConfig loaded = loader.load(plugin.getConfig());
        for (String warning : loader.warnings()) {
            log.warn("config.yml: " + warning);
        }
        return loaded;
    }

    private CloudClient createCloud(EventClock clock, Path dataDir, CloudListenerRelay relay) {
        PluginConfig.Cloud settings = config.cloud();
        if (!settings.enabled()) {
            cloudDetail = "disabled in config.yml";
            return null;
        }
        if (settings.licenseKey().isBlank()) {
            cloudDetail = "off (no cloud.license-key)";
            log.warn("no cloud.license-key configured: local checks run, nothing is sent to the cloud");
            return null;
        }
        EndpointPolicy.Result endpoint = EndpointPolicy.evaluate(settings.endpoint(), settings.allowInsecure());
        if (!endpoint.usable()) {
            cloudDetail = "off (endpoint refused)";
            log.warn(endpoint.message());
            return null;
        }
        if (endpoint.kind() == EndpointPolicy.Kind.INSECURE_ALLOWED) {
            log.warn(endpoint.message());
        }
        cloudDetail = endpoint.uri().getHost() + " (license " + Fingerprint.of(settings.licenseKey()) + ")";
        CloudConfig cloudConfig = new CloudConfig(endpoint.uri(), settings.licenseKey(), config.serverName(),
                config.networkId(), ServerPlatform.platform(), Bukkit.getVersion(),
                plugin.getPluginMeta().getVersion(), Bukkit.getMinecraftVersion(), config.shadow(),
                CloudClient.CAPABILITIES, Duration.ofMillis(settings.batchIntervalMs()),
                (long) settings.maxBufferMb() << 20, Duration.ofMinutes(settings.maxBufferAgeMinutes()), dataDir);
        return new CloudClient(cloudConfig, relay, log, clock, () -> sessions == null ? 0 : sessions.size());
    }

    private int hotLoad(long nanos) {
        int count = 0;
        for (Player player : Bukkit.getOnlinePlayers()) {
            User user = PacketEvents.getAPI().getPlayerManager().getUser(player);
            if (user != null && user.getUUID() != null) {
                sessions.hotLoad(user, ClientInfos.of(user), player, nanos);
                count++;
            }
        }
        return count;
    }
}
