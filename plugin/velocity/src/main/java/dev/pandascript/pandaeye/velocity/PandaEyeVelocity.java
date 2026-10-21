package dev.pandascript.pandaeye.velocity;

import com.google.inject.Inject;
import com.velocitypowered.api.command.CommandMeta;
import com.velocitypowered.api.event.PostOrder;
import com.velocitypowered.api.event.Subscribe;
import com.velocitypowered.api.event.proxy.ProxyInitializeEvent;
import com.velocitypowered.api.event.proxy.ProxyShutdownEvent;
import com.velocitypowered.api.network.ProtocolVersion;
import com.velocitypowered.api.plugin.annotation.DataDirectory;
import com.velocitypowered.api.proxy.ProxyServer;
import com.velocitypowered.api.scheduler.ScheduledTask;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.CloudClient;
import dev.pandascript.pandaeye.common.transport.CloudConfig;
import dev.pandascript.pandaeye.common.transport.CloudListener;
import dev.pandascript.pandaeye.common.transport.CloudStatus;
import dev.pandascript.pandaeye.common.transport.EndpointPolicy;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.Fingerprint;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.verdict.CircuitBreaker;
import dev.pandascript.pandaeye.common.verdict.ExecutedStore;
import dev.pandascript.pandaeye.common.verdict.VerdictProcessor;
import dev.pandascript.pandaeye.protocol.v1.Platform;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import org.slf4j.Logger;

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.TimeUnit;

/**
 * Velocity entry point (ARCHITECTURE 6): player tracking for the cloud through the shared transport, the signed
 * {@code pandaeye:session} message to backends, and verdict handling with the Paper plugin's rules (shadow by
 * default; in live mode with cloud enforcement, proxy commands for KICK/BAN). Shutdown is bounded (the
 * transport gets at most 250 ms and never blocks on the network).
 */
@SuppressWarnings("deprecation") // Subscribe.order: PostOrder works on every Velocity 3.x; priority needs 3.3+
public final class PandaEyeVelocity {

    private static final Duration STOP_BUDGET = Duration.ofMillis(250);

    private final ProxyServer server;
    private final Logger logger;
    private final Path dataDirectory;
    private volatile VelocityConfig config = VelocityConfig.DEFAULT;
    private volatile boolean active;
    private volatile VerdictProcessor verdicts;
    private Log log;
    private CloudClient cloud;
    private ProxyEnforcement enforcement;
    private ProxyTracker tracker;
    private CommandMeta commandMeta;
    private final List<ScheduledTask> tasks = new ArrayList<>();
    private volatile List<String> commandWarnings = List.of();
    private String cloudDetail = "disabled";

    @Inject
    public PandaEyeVelocity(ProxyServer server, Logger logger, @DataDirectory Path dataDirectory) {
        this.server = server;
        this.logger = logger;
        this.dataDirectory = dataDirectory;
    }

    @Subscribe
    public void onProxyInitialize(ProxyInitializeEvent event) {
        try {
            enable();
        } catch (Throwable t) {
            logger.error("PandaEye failed to enable and stays inactive", t);
            disable();
        }
    }

    @Subscribe(order = PostOrder.LAST)
    public void onProxyShutdown(ProxyShutdownEvent event) {
        disable();
    }

    private void enable() throws IOException {
        long started = System.nanoTime();
        log = new Slf4jLog(logger, () -> config.debug());
        config = loadConfig();
        RateLimitedLog errors = new RateLimitedLog(log, TimeUnit.SECONDS.toNanos(10));
        EventClock clock = EventClock.system();
        NameCache names = new NameCache();
        ConsoleAlerts alerts = new ConsoleAlerts(log);
        BedrockCheck bedrock = BedrockCheck.detect(PandaEyeVelocity.class.getClassLoader());
        cloud = createCloud(clock, new CloudListener() {
            @Override
            public void onVerdict(Verdict verdict) {
                VerdictProcessor processor = verdicts;
                if (processor != null) {
                    processor.handle(verdict);
                }
            }
        });
        CloudClient client = cloud;
        enforcement = new ProxyEnforcement(() -> config.live(),
                () -> client == null ? RemoteSettings.UNKNOWN : client.remoteSettings(),
                new ShadowProxyEnforcer(), new LiveProxyEnforcer(server, log));
        ExecutedStore store = ExecutedStore.load(dataDirectory.resolve(ExecutedStore.FILE_NAME), log,
                System.currentTimeMillis());
        verdicts = new VerdictProcessor(new VelocityVerdictPlatform(server, () -> config, enforcement, names, alerts),
                store, client == null ? (id, executed, note) -> { } : client::ackVerdict, log);
        tracker = new ProxyTracker(client == null ? null : client.sink()::offer, clock, () -> config, bedrock, names,
                () -> active, () -> System.currentTimeMillis() / 1_000, errors);

        active = true;
        server.getChannelRegistrar().register(ProxyTracker.SESSION_CHANNEL);
        server.getEventManager().register(this, tracker);
        commandMeta = server.getCommandManager().metaBuilder(ProxyCommand.NAME).aliases(ProxyCommand.ALIAS)
                .plugin(this).build();
        server.getCommandManager().register(commandMeta, new ProxyCommand(this));
        tracker.hotLoad(server.getAllPlayers());
        tasks.add(server.getScheduler().buildTask(this, tracker::announce)
                .delay(60, TimeUnit.SECONDS).repeat(60, TimeUnit.SECONDS).schedule());
        // Other plugins register their commands during their own ProxyInitializeEvent: check a moment later.
        tasks.add(server.getScheduler().buildTask(this, this::checkCommands).delay(2, TimeUnit.SECONDS).schedule());
        if (cloud != null) {
            cloud.start();
        }
        log.info(String.format(Locale.ROOT, "PandaEye for Velocity enabled in %d ms: mode %s, cloud %s, %d online"
                        + " players, session messages %s, Floodgate %s",
                TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - started), config.live() ? "live" : "shadow",
                cloudDetail, tracker.size(), config.sessionSecret().isEmpty() ? "off (no session-secret)" : "on",
                bedrock.floodgate() ? "found" : "not found"));
        if (config.shadow()) {
            log.info("shadow mode: PandaEye never kicks, bans or runs commands on the proxy");
        }
    }

    /** Bounded: unregisters everything and gives the transport at most 250 ms. Safe to call twice. */
    private void disable() {
        active = false;
        for (ScheduledTask task : tasks) {
            try {
                task.cancel();
            } catch (Throwable ignored) {
                // best effort
            }
        }
        tasks.clear();
        try {
            if (tracker != null) {
                server.getEventManager().unregisterListener(this, tracker);
                tracker.clear();
            }
            server.getChannelRegistrar().unregister(ProxyTracker.SESSION_CHANNEL);
            if (commandMeta != null) {
                server.getCommandManager().unregister(commandMeta);
                commandMeta = null;
            }
        } catch (Throwable t) {
            logger.warn("could not unregister PandaEye's listeners: {}", t.toString());
        }
        CloudClient current = cloud;
        cloud = null;
        if (current != null) {
            current.stop(STOP_BUDGET);
        }
        verdicts = null;
    }

    // ---------------------------------------------------------------------------------------------

    VerdictProcessor verdicts() {
        return verdicts;
    }

    List<String> statusLines() {
        List<String> lines = new ArrayList<>();
        RemoteSettings remote = cloud == null ? RemoteSettings.UNKNOWN : cloud.remoteSettings();
        lines.add("PandaEye (proxy) status");
        lines.add(String.format(Locale.ROOT, "mode %s, cloud enforcement %s%s, enforcing: %s",
                config.live() ? "live" : "shadow", remote.enforcement() ? "on" : "off",
                remote.known() ? " (as of " + Instant.ofEpochMilli(remote.receivedUnixMs()) + ")" : " (never received)",
                enforcement != null && enforcement.enabled() ? "yes" : "no"));
        CloudClient current = cloud;
        if (current == null) {
            lines.add("cloud: " + cloudDetail);
        } else {
            CloudStatus status = current.status();
            lines.add(String.format(Locale.ROOT, "cloud: %s - %s (server id %s), pending batches %d, dropped events %d",
                    status.state(), status.detail(), status.serverId(), status.pendingBatches(), status.droppedEvents()));
        }
        if (tracker != null) {
            lines.add(String.format(Locale.ROOT, "tracked players %d, session messages sent %d, forged messages dropped %d",
                    tracker.size(), tracker.sessionMessages(), tracker.droppedMessages()));
        }
        VerdictProcessor processor = verdicts;
        if (processor != null) {
            CircuitBreaker.Status breaker = processor.breakerStatus();
            lines.add(String.format(Locale.ROOT, "verdicts: circuit breaker %s, executed (7 d) %d, bans executed here %d",
                    breaker.open() ? "OPEN" : "closed", processor.store().executedCount(), processor.store().banCount()));
        }
        if (enforcement != null) {
            lines.add(String.format(Locale.ROOT, "enforcer calls attempted in shadow %d, executed live %d",
                    enforcement.shadow().calls(), enforcement.live().calls()));
        }
        for (String warning : commandWarnings) {
            lines.add("punishments: " + warning);
        }
        return lines;
    }

    private void checkCommands() {
        if (!active) {
            return;
        }
        List<String> warnings = ProxyCommandCheck.validate(config.punishments(),
                label -> server.getCommandManager().getCommandMeta(label));
        commandWarnings = List.copyOf(warnings);
        for (String warning : warnings) {
            log.warn("!!! " + warning + (config.shadow() ? " (shadow mode: nothing is executed until mode: live)" : "")
                    + " !!!");
        }
    }

    private VelocityConfig loadConfig() throws IOException {
        Path file = dataDirectory.resolve("config.yml");
        if (!Files.exists(file)) {
            Files.createDirectories(dataDirectory);
            try (InputStream defaults = PandaEyeVelocity.class.getResourceAsStream("/config.yml")) {
                if (defaults != null) {
                    Files.copy(defaults, file);
                }
            }
        }
        List<String> warnings = new ArrayList<>();
        VelocityConfig loaded = Files.exists(file)
                ? VelocityConfig.load(Files.readString(file, StandardCharsets.UTF_8), warnings)
                : VelocityConfig.DEFAULT;
        for (String warning : warnings) {
            log.warn("config.yml: " + warning);
        }
        return loaded;
    }

    private CloudClient createCloud(EventClock clock, CloudListener listener) {
        if (!config.cloudEnabled()) {
            cloudDetail = "disabled in config.yml";
            return null;
        }
        if (config.licenseKey().isBlank()) {
            cloudDetail = "off (no cloud.license-key)";
            log.warn("no cloud.license-key configured: nothing is sent to the cloud");
            return null;
        }
        EndpointPolicy.Result endpoint = EndpointPolicy.evaluate(config.endpoint(), config.allowInsecure());
        if (!endpoint.usable()) {
            cloudDetail = "off (endpoint refused)";
            log.warn(endpoint.message());
            return null;
        }
        if (endpoint.kind() == EndpointPolicy.Kind.INSECURE_ALLOWED) {
            log.warn(endpoint.message());
        }
        cloudDetail = endpoint.uri().getHost() + " (license " + Fingerprint.of(config.licenseKey()) + ")";
        String version = server.getPluginManager().fromInstance(this)
                .flatMap(container -> container.getDescription().getVersion()).orElse("unknown");
        CloudConfig cloudConfig = new CloudConfig(endpoint.uri(), config.licenseKey(), config.serverName(),
                config.networkId(), Platform.PLATFORM_VELOCITY,
                server.getVersion().getName() + " " + server.getVersion().getVersion(), version,
                ProtocolVersion.MAXIMUM_VERSION.getMostRecentSupportedVersion(), config.shadow(), List.of("probe"),
                Duration.ofMillis(config.batchIntervalMs()), (long) config.maxBufferMb() << 20,
                Duration.ofMinutes(config.maxBufferAgeMinutes()), dataDirectory);
        return new CloudClient(cloudConfig, listener, log, clock, server::getPlayerCount);
    }
}
