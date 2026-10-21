package dev.pandascript.pandaeye.paper.enforce;

import net.kyori.adventure.text.Component;
import org.bukkit.Bukkit;
import org.bukkit.Location;
import org.bukkit.entity.Player;
import org.bukkit.event.Cancellable;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerTeleportEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.plugin.Plugin;
import org.bukkit.util.Vector;

/**
 * Live mode: performs the side effects. The only class allowed to call teleport, kick, dispatchCommand,
 * setCancelled, setDamage and setVelocity (enforced by {@code EnforcerArchitectureTest}).
 */
public final class LiveEnforcer implements Enforcer {

    private final Plugin plugin;
    private final EnforcerCounters counters = new EnforcerCounters();

    public LiveEnforcer(Plugin plugin) {
        this.plugin = plugin;
    }

    @Override
    public boolean live() {
        return true;
    }

    @Override
    public void setback(Player player, Location target) {
        counters.count(EnforcerCounters.Action.SETBACK);
        player.getScheduler().run(plugin,
                task -> player.teleportAsync(target, PlayerTeleportEvent.TeleportCause.PLUGIN), null);
    }

    @Override
    public void kick(Player player, Component message) {
        counters.count(EnforcerCounters.Action.KICK);
        player.getScheduler().run(plugin, task -> player.kick(message), null);
    }

    @Override
    public void dispatchCommand(String command) {
        counters.count(EnforcerCounters.Action.COMMAND);
        Bukkit.getGlobalRegionScheduler().execute(plugin,
                () -> Bukkit.dispatchCommand(Bukkit.getConsoleSender(), command));
    }

    @Override
    public void cancel(Cancellable event) {
        counters.count(EnforcerCounters.Action.CANCEL);
        event.setCancelled(true);
    }

    @Override
    public void setDamage(EntityDamageEvent event, double damage) {
        counters.count(EnforcerCounters.Action.DAMAGE);
        event.setDamage(damage);
    }

    @Override
    public void setVelocity(PlayerVelocityEvent event, Vector velocity) {
        counters.count(EnforcerCounters.Action.VELOCITY);
        event.setVelocity(velocity);
    }

    @Override
    public EnforcerCounters counters() {
        return counters;
    }
}
