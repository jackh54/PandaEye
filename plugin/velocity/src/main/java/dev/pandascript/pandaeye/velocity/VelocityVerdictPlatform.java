package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ProxyServer;
import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;
import dev.pandascript.pandaeye.common.verdict.VerdictPlatform;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import net.kyori.adventure.text.Component;

import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;

/**
 * Verdict handling on the proxy (ARCHITECTURE 6): the same rules as the Paper plugin (shared
 * {@code VerdictProcessor}), with proxy commands for KICK/BAN in live mode with enforcement, disconnect when no
 * kick command is configured, and no mitigations (those are applied by the backends).
 */
final class VelocityVerdictPlatform implements VerdictPlatform {

    private final ProxyServer server;
    private final Supplier<VelocityConfig> config;
    private final ProxyEnforcement enforcement;
    private final NameCache names;
    private final ConsoleAlerts alerts;

    VelocityVerdictPlatform(ProxyServer server, Supplier<VelocityConfig> config, ProxyEnforcement enforcement,
                            NameCache names, ConsoleAlerts alerts) {
        this.server = server;
        this.config = config;
        this.enforcement = enforcement;
        this.names = names;
        this.alerts = alerts;
    }

    private Player online(UUID player) {
        return server.getPlayer(player).orElse(null);
    }

    @Override
    public boolean liveMode() {
        return enforcement.liveMode();
    }

    @Override
    public boolean cloudEnforcement() {
        return enforcement.cloudEnforcement();
    }

    @Override
    public PunishmentSettings punishments() {
        return config.get().punishments();
    }

    @Override
    public boolean mitigationsSupported() {
        return false;
    }

    @Override
    public boolean mitigationsEnabled() {
        return false;
    }

    @Override
    public long nowUnixMillis() {
        return System.currentTimeMillis();
    }

    @Override
    public String onlineName(UUID player) {
        Player online = online(player);
        return online == null ? null : online.getUsername();
    }

    @Override
    public String cachedName(UUID player) {
        return names.get(player);
    }

    @Override
    public boolean exempt(UUID player) {
        Player online = online(player);
        return online != null && online.hasPermission("pandaeye.bypass");
    }

    @Override
    public boolean kick(UUID player, String message) {
        Player online = online(player);
        ProxyEnforcer enforcer = enforcement.forFeature(true);
        if (online == null || !enforcer.live()) {
            return false;
        }
        enforcer.disconnect(online, Component.text(message));
        return true;
    }

    @Override
    public boolean dispatch(String command) {
        ProxyEnforcer enforcer = enforcement.forFeature(true);
        if (!enforcer.live() || command.isEmpty()) {
            return false;
        }
        enforcer.command(command);
        return true;
    }

    @Override
    public boolean dispatchUnban(String command) {
        if (command.isEmpty()) {
            return false;
        }
        enforcement.correctiveUnban().command(command);
        return true;
    }

    @Override
    public boolean mitigate(UUID player, Set<Mitigation> mitigations, double strength, String verdictId) {
        return false;
    }

    @Override
    public boolean unmitigate(UUID player) {
        return false;
    }

    @Override
    public void alert(String key, String text) {
        alerts.alert(text);
    }

    @Override
    public void urgent(String text) {
        alerts.urgent(text);
    }
}
