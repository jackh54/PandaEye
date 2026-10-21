package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ProxyServer;
import dev.pandascript.pandaeye.common.Log;
import net.kyori.adventure.text.Component;

import java.util.concurrent.atomic.LongAdder;

/** Live mode: the only class that disconnects players or runs proxy commands. Velocity's APIs are thread-safe. */
final class LiveProxyEnforcer implements ProxyEnforcer {

    private final ProxyServer server;
    private final Log log;
    private final LongAdder calls = new LongAdder();

    LiveProxyEnforcer(ProxyServer server, Log log) {
        this.server = server;
        this.log = log;
    }

    @Override
    public boolean live() {
        return true;
    }

    @Override
    public void disconnect(Player player, Component message) {
        calls.increment();
        player.disconnect(message);
    }

    @Override
    public void command(String command) {
        calls.increment();
        server.getCommandManager().executeAsync(server.getConsoleCommandSource(), command)
                .whenComplete((found, error) -> {
                    if (error != null) {
                        log.warn("proxy command failed: " + command, error);
                    } else if (!Boolean.TRUE.equals(found)) {
                        log.warn("proxy command not found: " + command);
                    }
                });
    }

    @Override
    public long calls() {
        return calls.sum();
    }
}
