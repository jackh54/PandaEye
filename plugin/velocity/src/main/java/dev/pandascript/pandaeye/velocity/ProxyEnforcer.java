package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.proxy.Player;
import net.kyori.adventure.text.Component;

/**
 * Every side effect the proxy plugin can have on a player (disconnect, proxy command) goes through this
 * interface, like the Paper plugin's Enforcer: the shadow implementation only counts; the live one is handed
 * out by {@link ProxyEnforcement} only in live mode with cloud enforcement (and for the UNBAN exception).
 * {@code ProxyEnforcerArchitectureTest} asserts that no other class calls the underlying APIs.
 */
interface ProxyEnforcer {

    boolean live();

    /** Disconnects an online player (KICK). Any thread. */
    void disconnect(Player player, Component message);

    /** Runs a proxy console command (KICK/BAN/UNBAN commands). Any thread. */
    void command(String command);

    /** Calls made (attempted in shadow, executed in live). */
    long calls();
}
