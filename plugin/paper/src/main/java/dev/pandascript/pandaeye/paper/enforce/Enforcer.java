package dev.pandascript.pandaeye.paper.enforce;

import net.kyori.adventure.text.Component;
import org.bukkit.Location;
import org.bukkit.entity.Player;
import org.bukkit.event.Cancellable;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerVelocityEvent;

/**
 * Every side effect that could affect a player goes through this interface (ARCHITECTURE 1.1 and 4.2). The
 * shadow implementation only counts attempted calls; the live implementation performs them and is handed
 * out only when local mode is live AND the cloud's last-known enforcement switch is on AND the feature's own
 * switch is on. An ArchUnit test asserts that no other class calls the underlying APIs.
 */
public interface Enforcer {

    /** True for the implementation that really acts. */
    boolean live();

    /** Sets a player back (teleportAsync on the player's entity thread). Any thread. */
    void setback(Player player, Location target);

    /** Kicks an online player on the player's entity thread. Any thread. */
    void kick(Player player, Component message);

    /** Runs a console command on the global region thread. Any thread. */
    void dispatchCommand(String command);

    /** Cancels a Bukkit event (mitigation listeners). Event thread only. */
    void cancel(Cancellable event);

    /** Changes the damage of an event (DAMAGE_REDUCE). Event thread only. */
    void setDamage(EntityDamageEvent event, double damage);

    /** Changes the velocity of a PlayerVelocityEvent (VELOCITY_BOOST). Event thread only. */
    void setVelocity(PlayerVelocityEvent event, org.bukkit.util.Vector velocity);

    EnforcerCounters counters();
}
