package dev.pandascript.pandaeye.paper.command;

import dev.pandascript.pandaeye.common.transport.CloudClient;
import dev.pandascript.pandaeye.common.transport.CloudStatus;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.paper.enforce.EnforcerCounters;
import dev.pandascript.pandaeye.paper.packet.PacketCost;
import dev.pandascript.pandaeye.paper.runtime.Runtime;
import dev.pandascript.pandaeye.paper.session.Session;
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

/**
 * {@code /pandaeye status | alerts [on|off] | player <name> | debug <name> | breaker [reset] | forget <uuid> |
 * reload} (ARCHITECTURE 4.10). Reads only thread-safe state.
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
        switch (sub) {
            case "status" -> status(sender);
            case "alerts" -> alerts(sender, args);
            case "reload" -> reload(sender);
            case "player", "debug", "breaker", "forget" -> {
                if (requireAdmin(sender)) {
                    reply(sender, "/" + label + " " + sub + " is not available yet.", NamedTextColor.YELLOW);
                }
            }
            default -> reply(sender, "Usage: /" + label + " " + String.join("|", SUBCOMMANDS), NamedTextColor.RED);
        }
        return true;
    }

    @Override
    public List<String> onTabComplete(CommandSender sender, Command command, String alias, String[] args) {
        if (args.length == 1) {
            List<String> matches = new ArrayList<>();
            for (String sub : SUBCOMMANDS) {
                if (sub.startsWith(args[0].toLowerCase(Locale.ROOT))) {
                    matches.add(sub);
                }
            }
            return matches;
        }
        if (args.length == 2 && args[0].equalsIgnoreCase("alerts")) {
            return List.of("on", "off");
        }
        return List.of();
    }

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
        for (Session session : runtime.sessions().all()) {
            if (!session.publishedSeeded()) {
                unseeded++;
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
        reply(sender, "Reloaded check thresholds and alert settings (mode and cloud settings need a restart).",
                NamedTextColor.GREEN);
        for (String warning : warnings) {
            reply(sender, "config.yml: " + warning, NamedTextColor.YELLOW);
        }
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
