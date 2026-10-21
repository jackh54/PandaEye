package dev.pandascript.pandaeye.paper.listener;

import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.env.DigProbes;
import dev.pandascript.pandaeye.paper.packet.ActiveGate;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import org.bukkit.event.EventHandler;
import org.bukkit.event.EventPriority;
import org.bukkit.event.Listener;
import org.bukkit.event.block.BlockDamageEvent;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.event.player.PlayerQuitEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.util.Vector;

/**
 * Read-only MONITOR listeners: join/quit for the session lifecycle, PlayerVelocityEvent as pending velocity,
 * BlockDamageEvent (the server handling a dig START while the block still exists) as the dig.speed probe.
 */
public final class BukkitListeners implements Listener {

    private final ActiveGate gate;
    private final SessionRegistry sessions;
    private final RateLimitedLog errors;

    public BukkitListeners(ActiveGate gate, SessionRegistry sessions, RateLimitedLog errors) {
        this.gate = gate;
        this.sessions = sessions;
        this.errors = errors;
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onJoin(PlayerJoinEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            sessions.playerJoined(event.getPlayer());
        } catch (Throwable t) {
            errors.warn("join", "join handling failed", t);
        } finally {
            gate.exit();
        }
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onQuit(PlayerQuitEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            sessions.quit(event.getPlayer().getUniqueId(), event.getReason().name().toLowerCase(java.util.Locale.ROOT));
        } catch (Throwable t) {
            errors.warn("quit", "quit handling failed", t);
        } finally {
            gate.exit();
        }
    }

    @EventHandler(priority = EventPriority.MONITOR, ignoreCancelled = true)
    public void onVelocity(PlayerVelocityEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            Session session = sessions.get(event.getPlayer().getUniqueId());
            if (session != null) {
                Vector velocity = event.getVelocity();
                session.bukkitVelocity(velocity.getX(), velocity.getY(), velocity.getZ(), System.nanoTime());
            }
        } catch (Throwable t) {
            errors.warn("velocity", "velocity handling failed", t);
        } finally {
            gate.exit();
        }
    }

    /** Cancelled events count too: the client digs on regardless of what the server or other plugins decide. */
    @EventHandler(priority = EventPriority.MONITOR)
    public void onBlockDamage(BlockDamageEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            Session session = sessions.get(event.getPlayer().getUniqueId());
            if (session != null) {
                session.signals().digProbe(DigProbes.take(event.getPlayer(), event.getBlock(), event.getInstaBreak(),
                        System.nanoTime()));
            }
        } catch (Throwable t) {
            errors.warn("dig", "dig probe failed", t);
        } finally {
            gate.exit();
        }
    }
}
