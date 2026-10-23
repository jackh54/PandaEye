package dev.pandascript.pandaeye.paper.command;

import dev.pandascript.pandaeye.common.transport.CloudClient;
import dev.pandascript.pandaeye.common.transport.CloudStatus;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.common.verdict.CircuitBreaker;
import dev.pandascript.pandaeye.paper.check.CheckHandle;
import dev.pandascript.pandaeye.paper.enforce.EnforcerCounters;
import dev.pandascript.pandaeye.paper.packet.PacketCost;
import dev.pandascript.pandaeye.paper.runtime.Runtime;
import dev.pandascript.pandaeye.paper.session.MitigationState;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.Exemption;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.verdict.VerdictHandler;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.format.NamedTextColor;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.command.TabExecutor;
import org.bukkit.entity.Player;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.UUID;
import java.util.concurrent.TimeUnit;

/**
 * {@code /pandaeye status | alerts [on|off] | player <name> | debug <name> | breaker [reset] | forget <uuid> |
 * reload} (ARCHITECTURE 4.10). Reads only thread-safe state; per-player packet state is read on the player's
 * netty thread and the answer is delivered on the sender's own thread. {@code pandaeye.command} for reading,
 * {@code pandaeye.admin} for {@code breaker reset}, {@code forget} and {@code reload}. Nothing here changes
 * how any player is treated.
 */
public final class PandaEyeCommand implements TabExecutor {

    private static final List<String> SUBCOMMANDS = List.of("status", "alerts", "player", "debug", "breaker",
            "forget", "reload");

    private final Runtime runtime;

    public PandaEyeCommand(Runtime runtime) {
        this.runtime = runtime;
    }

    @Override
    public boolean onCommand(CommandSender sender, Command command, String label, String[] args) {
        String sub = args.length == 0 ? "status" : args[0].toLowerCase(Locale.ROOT);
        try {
            switch (sub) {
                case "status" -> status(sender);
                case "alerts" -> alerts(sender, args);
                case "player" -> player(sender, label, args, false);
                case "debug" -> player(sender, label, args, true);
                case "breaker" -> breaker(sender, label, args);
                case "forget" -> forget(sender, label, args);
                case "reload" -> reload(sender);
                default -> reply(sender, "Usage: /" + label + " " + String.join("|", SUBCOMMANDS), NamedTextColor.RED);
            }
        } catch (Throwable t) {
            runtime.errors().warn("command", "/" + label + " " + sub + " failed", t);
            reply(sender, "/" + label + " " + sub + " failed: " + t.getClass().getSimpleName(), NamedTextColor.RED);
        }
        return true;
    }

    @Override
    public List<String> onTabComplete(CommandSender sender, Command command, String alias, String[] args) {
        if (args.length == 1) {
            return matching(SUBCOMMANDS, args[0]);
        }
        if (args.length == 2) {
            return switch (args[0].toLowerCase(Locale.ROOT)) {
                case "alerts" -> matching(List.of("on", "off"), args[1]);
                case "breaker" -> matching(List.of("reset"), args[1]);
                case "player", "debug" -> {
                    List<String> names = new ArrayList<>();
                    for (Session session : runtime.sessions().all()) {
                        names.add(session.name());
                    }
                    yield matching(names, args[1]);
                }
                default -> List.of();
            };
        }
        return List.of();
    }

    // ---------------------------------------------------------------------------------------------
    // status, alerts, reload
    // ---------------------------------------------------------------------------------------------

    private void status(CommandSender sender) {
        RemoteSettings remote = runtime.remoteSettings();
        List<String> lines = new ArrayList<>();
        lines.add("PandaEye status");
        lines.add(String.format(Locale.ROOT, "mode %s, cloud enforcement %s%s, enforcing: %s",
                runtime.config().mode().name().toLowerCase(Locale.ROOT), remote.enforcement() ? "on" : "off",
                remote.known() ? " (as of " + Instant.ofEpochMilli(remote.receivedUnixMs()) + ")" : " (never received)",
                runtime.enforcement().enabled() ? "yes" : "no"));
        CloudClient cloud = runtime.cloud();
        if (cloud == null) {
            lines.add("cloud: " + runtime.cloudDetail());
        } else {
            CloudStatus status = cloud.status();
            lines.add(String.format(Locale.ROOT, "cloud: %s - %s (server id %s, %s)", status.state(), status.detail(),
                    status.serverId(), status.compression()));
            lines.add(String.format(Locale.ROOT, "connection RTT %.1f ms, probe RTT last %.1f / p50 %.1f / p95 %.1f ms",
                    status.connectionRttMicros() / 1e3, status.lastProbeRttMicros() / 1e3,
                    status.probeP50Micros() / 1e3, status.probeP95Micros() / 1e3));
            lines.add(String.format(Locale.ROOT, "pending batches %d (%d replay, %.1f MiB), queued events %d, sent %d, "
                            + "last acked seq %d", status.pendingBatches(), status.replayBatches(),
                    status.pendingBytes() / 1048576.0, status.queuedEvents(), status.sentBatches(),
                    status.lastAckedSeq()));
            lines.add(String.format(Locale.ROOT, "dropped: %d events (queue full), %d batches / %d events (buffer), "
                            + "%d oversize events, %d rejected batches", status.droppedEvents(), status.droppedBatches(),
                    status.droppedBatchEvents(), status.oversizeEvents(), status.tooLargeBatches()));
        }
        int unseeded = 0;
        int mitigated = 0;
        for (Session session : runtime.sessions().all()) {
            if (!session.publishedSeeded()) {
                unseeded++;
            }
            if (session.mitigation().active()) {
                mitigated++;
            }
        }
        lines.add(String.format(Locale.ROOT, "tracked players %d (%d unseeded), watched %d, alert recipients %d",
                runtime.sessions().size(), unseeded, runtime.watches().active(), runtime.alerts().recipients().size()));
        PacketCost.Summary cost = runtime.cost().summary();
        lines.add(String.format(Locale.ROOT, "packet handler: %d packets, avg %.2f us, p99 %.2f us; env sampler avg %.1f us",
                cost.packets(), cost.averageMicros(), cost.p99Micros(), runtime.sampler().averageCostNanos() / 1e3));
        lines.add(String.format(Locale.ROOT, "flags confirmed %d, confirmations dropped %d, discarded %d, stalls %d, "
                        + "log lines dropped %d, callback errors %d", runtime.flags().confirmedTotal(),
                runtime.confirmations().dropped(), runtime.confirmations().discarded(), runtime.stalls().stalls(),
                runtime.logs().dropped(), runtime.errors().total()));
        VerdictHandler verdicts = runtime.verdicts();
        if (verdicts != null) {
            CircuitBreaker.Status breaker = verdicts.breaker();
            lines.add(String.format(Locale.ROOT, "verdicts: circuit breaker %s, executed (7 d) %d, bans executed here %d,"
                            + " mitigated players %d, mitigation listeners %s", breaker.open() ? "OPEN" : "closed",
                    verdicts.store().executedCount(), verdicts.store().banCount(), mitigated,
                    verdicts.mitigations().registered() ? "registered" : "not registered"));
            if (!verdicts.commandWarnings().isEmpty()) {
                lines.add("punishment commands: " + verdicts.commandWarnings().size() + " problem(s): "
                        + String.join("; ", verdicts.commandWarnings()));
            }
        }
        lines.add("enforcer calls attempted in shadow: " + counts(runtime.enforcement().shadow().counters())
                + "; executed live: " + counts(runtime.enforcement().live().counters()));
        for (String line : lines) {
            reply(sender, line, NamedTextColor.GRAY);
        }
    }

    private void alerts(CommandSender sender, String[] args) {
        if (!(sender instanceof Player player)) {
            reply(sender, "Only players can toggle in-game alerts.", NamedTextColor.RED);
            return;
        }
        if (!player.hasPermission("pandaeye.alerts")) {
            reply(sender, "You lack pandaeye.alerts.", NamedTextColor.RED);
            return;
        }
        Boolean on = args.length < 2 ? null : switch (args[1].toLowerCase(Locale.ROOT)) {
            case "on" -> Boolean.TRUE;
            case "off" -> Boolean.FALSE;
            default -> null;
        };
        boolean enabled = runtime.alerts().recipients().toggle(player, on);
        reply(sender, "PandaEye alerts " + (enabled ? "on" : "off") + ".", NamedTextColor.GREEN);
    }

    private void reload(CommandSender sender) {
        if (!requireAdmin(sender)) {
            return;
        }
        List<String> warnings = runtime.reload();
        reply(sender, "Reloaded check thresholds and alert settings (mode, cloud, punishment and mitigation settings"
                + " need a restart).", NamedTextColor.GREEN);
        for (String warning : warnings) {
            reply(sender, "config.yml: " + warning, NamedTextColor.YELLOW);
        }
    }

    // ---------------------------------------------------------------------------------------------
    // player, debug
    // ---------------------------------------------------------------------------------------------

    private void player(CommandSender sender, String label, String[] args, boolean debug) {
        String sub = debug ? "debug" : "player";
        if (args.length < 2) {
            reply(sender, "Usage: /" + label + " " + sub + " <name>", NamedTextColor.RED);
            return;
        }
        Session session = find(args[1]);
        if (session == null) {
            reply(sender, "No tracked player '" + args[1] + "' on this server.", NamedTextColor.RED);
            return;
        }
        long now = System.nanoTime();
        List<String> lines = new ArrayList<>();
        Boolean bypass = session.signals().bypass();
        lines.add(String.format(Locale.ROOT, "%s %s: session %s, %s %d s ago, seeded %s, bedrock %s, bypass %s",
                session.name(), session.uuid(), session.id().toString().substring(0, 8),
                session.hotLoaded() ? "hot-loaded" : "joined", TimeUnit.NANOSECONDS.toSeconds(now - session.createdNanos()),
                yesNo(session.publishedSeeded()), yesNo(session.bedrock()),
                bypass == null ? "not evaluated" : yesNo(bypass)));
        lines.add(String.format(Locale.ROOT, "client %s (protocol %d, tick client %s), network session %s",
                session.client().version(), session.client().protocol(), yesNo(session.client().tickClient()),
                session.networkSession().isEmpty() ? "none" : session.networkSession()));
        MitigationState mitigation = session.mitigation();
        String watched = session.watched(now)
                ? ", watched for " + TimeUnit.NANOSECONDS.toSeconds(session.watchUntilNanos() - now) + " more s" : "";
        lines.add(mitigation.active()
                ? String.format(Locale.ROOT, "mitigations %s at strength %.2f (verdict %s)%s", mitigations(mitigation),
                mitigation.strength(), mitigation.verdictId(), watched)
                : "mitigations none" + watched);
        deliver(sender, lines, NamedTextColor.GRAY);
        runtime.lifecycle().onNetty(session, nanos -> deliver(sender, nettyLines(session, debug, nanos),
                NamedTextColor.GRAY));
    }

    /** Netty thread of the session: reads its packet state and check VLs. */
    private static List<String> nettyLines(Session session, boolean debug, long now) {
        PacketState state = session.state();
        List<String> lines = new ArrayList<>();
        lines.add(String.format(Locale.ROOT, "ping %d ms (high %d ms), client tick %d, brand %s",
                state.rtt().pingMillis(now), state.rtt().rttHighMillis(now), state.tick(),
                state.brand() == null ? "unknown" : state.brand()));
        StringBuilder vls = new StringBuilder();
        if (session.processor() != null) {
            for (CheckHandle handle : session.checks().handles()) {
                double vl = handle.violationLevel().value(now);
                if (vl > 0.005 || debug) {
                    if (!vls.isEmpty()) {
                        vls.append(", ");
                    }
                    vls.append(String.format(Locale.ROOT, "%s %.2f", handle.descriptor().id(), vl));
                }
            }
        }
        lines.add("VL " + (vls.isEmpty() ? "all 0" : vls));
        if (!debug) {
            return lines;
        }
        Exemption exemption = state.movementExemption(now);
        lines.add(String.format(Locale.ROOT, "movement checks: %s; moves since seed %d, epoch %d",
                exemption == null ? "running" : "exempt (" + exemption.name().toLowerCase(Locale.ROOT) + ")",
                state.movesSinceSeed(), state.epoch()));
        lines.add(String.format(Locale.ROOT, "position %s %.3f %.3f %.3f rot %.1f/%.1f, on ground %s, world %s",
                state.positionKnown() ? "" : "(unknown)", state.x(), state.y(), state.z(), state.yaw(), state.pitch(),
                yesNo(state.onGround()), state.worldName()));
        Abilities abilities = state.abilities().latest();
        lines.add(String.format(Locale.ROOT, "game mode %s (applied %s), abilities %s, in vehicle %s, dead %s, "
                        + "sprinting %s, sneaking %s", state.gameMode().latest(), state.gameMode().applied(),
                abilities == null ? "unknown" : abilities, state.inVehicle().latest(), yesNo(state.dead()),
                yesNo(state.sprinting()), yesNo(state.sneaking())));
        lines.add(String.format(Locale.ROOT, "tick rate %.1f%s, loading %s, unconfirmed teleports %d, velocity pending %s, "
                        + "open container %s", state.tickRate(), state.frozen() ? " (frozen)" : "", yesNo(state.loading()),
                state.teleports().unconfirmed(), yesNo(state.velocities().pending()),
                state.containers().known() ? state.containers().openId() + " " + state.containers().openType() : "unknown"));
        lines.add(String.format(Locale.ROOT, "movement speed %.4f, step %.2f, scale %.2f, effects %s",
                state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED),
                state.attributes().lenientMax(AttributeTracker.STEP_HEIGHT),
                state.attributes().lenientMax(AttributeTracker.SCALE), state.effects().latest()));
        return lines;
    }

    private Session find(String query) {
        for (Session session : runtime.sessions().all()) {
            if (session.name().equalsIgnoreCase(query)) {
                return session;
            }
        }
        if (Uuids.isCanonical(query.toLowerCase(Locale.ROOT))) {
            return runtime.sessions().get(UUID.fromString(query));
        }
        return null;
    }

    // ---------------------------------------------------------------------------------------------
    // breaker, forget
    // ---------------------------------------------------------------------------------------------

    private void breaker(CommandSender sender, String label, String[] args) {
        VerdictHandler verdicts = runtime.verdicts();
        if (verdicts == null) {
            reply(sender, "Verdict handling is not running.", NamedTextColor.RED);
            return;
        }
        if (args.length >= 2) {
            if (!args[1].equalsIgnoreCase("reset")) {
                reply(sender, "Usage: /" + label + " breaker [reset]", NamedTextColor.RED);
                return;
            }
            if (!requireAdmin(sender)) {
                return;
            }
            boolean wasOpen = verdicts.breaker().open();
            verdicts.resetBreaker(sender.getName());
            reply(sender, wasOpen ? "Circuit breaker reset: kicks, bans and mitigations resume."
                    : "Circuit breaker was closed; its counters were reset.", NamedTextColor.GREEN);
            return;
        }
        CircuitBreaker.Status status = verdicts.breaker();
        reply(sender, status.open()
                ? String.format(Locale.ROOT, "Circuit breaker OPEN since %s: %s. No kicks, bans or mitigations until"
                + " /%s breaker reset.", Instant.ofEpochMilli(status.openSinceUnixMs()), status.reason(), label)
                : "Circuit breaker closed.", status.open() ? NamedTextColor.RED : NamedTextColor.GREEN);
        reply(sender, String.format(Locale.ROOT, "bans in the last hour %d/%d, kicks in the last 10 min %d/%d",
                status.bansLastHour(), status.maxBansPerHour(), status.kicksLast10Min(), status.maxKicksPer10Min()),
                NamedTextColor.GRAY);
    }

    private void forget(CommandSender sender, String label, String[] args) {
        if (!requireAdmin(sender)) {
            return;
        }
        if (args.length < 2 || !Uuids.isCanonical(args[1].toLowerCase(Locale.ROOT))) {
            reply(sender, "Usage: /" + label + " forget <uuid> (a full UUID such as 069a79f4-44e9-4726-a5be-fca90e38aaf5)",
                    NamedTextColor.RED);
            return;
        }
        UUID player = UUID.fromString(args[1]);
        runtime.alerts().forget(player);
        boolean queued = runtime.logs().forget(player, result -> deliver(sender, List.of(String.format(Locale.ROOT,
                "Removed %d line(s) about %s from %d local log file(s)%s.", result.lines(), player, result.files(),
                result.failed() ? "; some files could not be rewritten (see console)" : "")),
                result.failed() ? NamedTextColor.YELLOW : NamedTextColor.GREEN));
        reply(sender, queued
                ? "Removing " + player + " from the local logs... (executed.json keeps the bans executed here so they"
                + " can still be lifted; the cloud's data is erased through the dashboard or API)"
                : "The log writer is busy or stopped; try again.", queued ? NamedTextColor.GRAY : NamedTextColor.RED);
    }

    // ---------------------------------------------------------------------------------------------

    /** Sends lines from any thread: on the player's own thread for players, directly to the console. */
    private void deliver(CommandSender sender, List<String> lines, NamedTextColor color) {
        if (sender instanceof Player player) {
            runtime.schedulers().runOnEntity(player, player.getUniqueId(), task -> {
                for (String line : lines) {
                    reply(player, line, color);
                }
            }, null, 0);
        } else {
            for (String line : lines) {
                reply(sender, line, color);
            }
        }
    }

    private static String mitigations(MitigationState state) {
        List<String> names = new ArrayList<>();
        for (Mitigation mitigation : state.mitigations()) {
            names.add(mitigation.name().replace("MITIGATION_", ""));
        }
        names.sort(null);
        return String.join(", ", names);
    }

    private static List<String> matching(List<String> options, String prefix) {
        List<String> matches = new ArrayList<>();
        String lower = prefix.toLowerCase(Locale.ROOT);
        for (String option : options) {
            if (option.toLowerCase(Locale.ROOT).startsWith(lower)) {
                matches.add(option);
            }
        }
        return matches;
    }

    private static String yesNo(boolean value) {
        return value ? "yes" : "no";
    }

    private static boolean requireAdmin(CommandSender sender) {
        if (sender.hasPermission("pandaeye.admin")) {
            return true;
        }
        reply(sender, "You lack pandaeye.admin.", NamedTextColor.RED);
        return false;
    }

    private static String counts(EnforcerCounters counters) {
        StringBuilder out = new StringBuilder();
        for (EnforcerCounters.Action action : EnforcerCounters.Action.values()) {
            if (!out.isEmpty()) {
                out.append(", ");
            }
            out.append(action.name().toLowerCase(Locale.ROOT)).append(' ').append(counters.get(action));
        }
        return out.toString();
    }

    private static void reply(CommandSender sender, String text, NamedTextColor color) {
        sender.sendMessage(Component.text(text, color));
    }
}
