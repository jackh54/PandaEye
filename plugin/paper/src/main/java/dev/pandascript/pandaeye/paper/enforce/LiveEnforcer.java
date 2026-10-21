package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.paper.session.SetbackTarget;
import net.kyori.adventure.text.Component;
import org.bukkit.Bukkit;
import org.bukkit.Location;
import org.bukkit.entity.Player;
import org.bukkit.event.Cancellable;
import org.bukkit.event.HandlerList;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerTeleportEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.plugin.Plugin;
import org.bukkit.util.Vector;

import java.util.function.Consumer;
import java.util.function.DoubleSupplier;

/**
 * Live mode: performs the side effects. The only class allowed to call teleport, kick, dispatchCommand,
 * setCancelled, setDamage and setVelocity, and the only one that creates and registers the
 * {@link MitigationListener} (enforced by {@code EnforcerArchitectureTest}). It is handed out by
 * {@link Enforcement} only in live mode with enforcement, except for the UNBAN exception
 * ({@link Enforcement#correctiveUnban()}).
 */
public final class LiveEnforcer implements Enforcer {

    private final Plugin plugin;
    private final Consumer<Runnable> globalRegion;
    private final EnforcerCounters counters = new EnforcerCounters();

    public LiveEnforcer(Plugin plugin) {
        this(plugin, task -> Bukkit.getGlobalRegionScheduler().execute(plugin, task));
    }

    /** {@code globalRegion} runs a task on the global region thread (tests record instead). */
    public LiveEnforcer(Plugin plugin, Consumer<Runnable> globalRegion) {
        this.plugin = plugin;
        this.globalRegion = globalRegion;
    }

    @Override
    public boolean live() {
        return true;
    }

    @Override
    public void setback(Player player, SetbackTarget target) {
        counters.count(EnforcerCounters.Action.SETBACK);
        player.getScheduler().run(plugin, task -> guarded("setback", () -> {
            if (player.getWorld().getUID().equals(target.worldUid())) {
                Location location = new Location(player.getWorld(), target.x(), target.y(), target.z(),
                        target.yaw(), target.pitch());
                player.teleportAsync(location, PlayerTeleportEvent.TeleportCause.PLUGIN);
            }
        }), null);
    }

    @Override
    public void kick(Player player, Component message) {
        counters.count(EnforcerCounters.Action.KICK);
        player.getScheduler().run(plugin, task -> guarded("kick", () -> player.kick(message)), null);
    }

    @Override
    public void dispatchCommand(String command) {
        counters.count(EnforcerCounters.Action.COMMAND);
        globalRegion.accept(() -> guarded("command",
                () -> Bukkit.dispatchCommand(Bukkit.getConsoleSender(), command)));
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
    public Registration registerMitigations(MitigationSource source, DoubleSupplier random) {
        counters.count(EnforcerCounters.Action.LISTENERS);
        MitigationListener listener = new MitigationListener(this, source, random);
        Bukkit.getPluginManager().registerEvents(listener, plugin);
        return new Registration() {
            private volatile boolean active = true;

            @Override
            public boolean active() {
                return active;
            }

            @Override
            public void close() {
                if (active) {
                    active = false;
                    HandlerList.unregisterAll(listener);
                }
            }
        };
    }

    @Override
    public EnforcerCounters counters() {
        return counters;
    }

    /** Scheduled bodies never throw into a region thread. */
    private void guarded(String action, Runnable body) {
        try {
            body.run();
        } catch (Throwable t) {
            plugin.getLogger().warning("PandaEye " + action + " failed: " + t);
        }
    }
}
