package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.command.CommandSource;
import com.velocitypowered.api.command.SimpleCommand;
import dev.pandascript.pandaeye.common.verdict.CircuitBreaker;
import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.format.NamedTextColor;

import java.time.Instant;
import java.util.List;
import java.util.Locale;

/**
 * {@code /pandaeyeproxy status | breaker [reset]} (alias {@code /peyeproxy}). A separate name so the backends'
 * {@code /pandaeye} keeps working through the proxy. {@code pandaeye.command} to read, {@code pandaeye.admin} to
 * reset the circuit breaker.
 */
final class ProxyCommand implements SimpleCommand {

    static final String NAME = "pandaeyeproxy";
    static final String ALIAS = "peyeproxy";

    private final PandaEyeVelocity plugin;

    ProxyCommand(PandaEyeVelocity plugin) {
        this.plugin = plugin;
    }

    @Override
    public void execute(Invocation invocation) {
        CommandSource source = invocation.source();
        String[] args = invocation.arguments();
        String sub = args.length == 0 ? "status" : args[0].toLowerCase(Locale.ROOT);
        try {
            switch (sub) {
                case "status" -> {
                    for (String line : plugin.statusLines()) {
                        source.sendMessage(Component.text(line, NamedTextColor.GRAY));
                    }
                }
                case "breaker" -> breaker(source, args);
                default -> source.sendMessage(Component.text("Usage: /" + NAME + " status|breaker [reset]",
                        NamedTextColor.RED));
            }
        } catch (Throwable t) {
            source.sendMessage(Component.text("/" + NAME + " " + sub + " failed: " + t.getClass().getSimpleName(),
                    NamedTextColor.RED));
        }
    }

    private void breaker(CommandSource source, String[] args) {
        if (plugin.verdicts() == null) {
            source.sendMessage(Component.text("Verdict handling is not running.", NamedTextColor.RED));
            return;
        }
        if (args.length >= 2 && args[1].equalsIgnoreCase("reset")) {
            if (!source.hasPermission("pandaeye.admin")) {
                source.sendMessage(Component.text("You lack pandaeye.admin.", NamedTextColor.RED));
                return;
            }
            plugin.verdicts().resetBreaker(source instanceof com.velocitypowered.api.proxy.Player player
                    ? player.getUsername() : "console");
            source.sendMessage(Component.text("Circuit breaker reset.", NamedTextColor.GREEN));
            return;
        }
        CircuitBreaker.Status status = plugin.verdicts().breakerStatus();
        source.sendMessage(Component.text(status.open()
                ? "Circuit breaker OPEN since " + Instant.ofEpochMilli(status.openSinceUnixMs()) + ": " + status.reason()
                : "Circuit breaker closed.", status.open() ? NamedTextColor.RED : NamedTextColor.GREEN));
        source.sendMessage(Component.text(String.format(Locale.ROOT, "bans in the last hour %d/%d, kicks in the last"
                        + " 10 min %d/%d", status.bansLastHour(), status.maxBansPerHour(), status.kicksLast10Min(),
                status.maxKicksPer10Min()), NamedTextColor.GRAY));
    }

    @Override
    public boolean hasPermission(Invocation invocation) {
        return invocation.source().hasPermission("pandaeye.command");
    }

    @Override
    public List<String> suggest(Invocation invocation) {
        String[] args = invocation.arguments();
        if (args.length <= 1) {
            String prefix = args.length == 0 ? "" : args[0].toLowerCase(Locale.ROOT);
            return List.of("status", "breaker").stream().filter(s -> s.startsWith(prefix)).toList();
        }
        if (args.length == 2 && args[0].equalsIgnoreCase("breaker")) {
            return List.of("reset");
        }
        return List.of();
    }
}
