package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.paper.session.SetbackTarget;
import net.kyori.adventure.text.Component;
import org.bukkit.entity.Player;
import org.bukkit.event.Cancellable;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.util.Vector;

/** Shadow mode: does nothing, counts what would have been done. */
public final class ShadowEnforcer implements Enforcer {

    private final EnforcerCounters counters = new EnforcerCounters();

    @Override
    public boolean live() {
        return false;
    }

    @Override
    public void setback(Player player, SetbackTarget target) {
        counters.count(EnforcerCounters.Action.SETBACK);
    }

    @Override
    public void kick(Player player, Component message) {
        counters.count(EnforcerCounters.Action.KICK);
    }

    @Override
    public void dispatchCommand(String command) {
        counters.count(EnforcerCounters.Action.COMMAND);
    }

    @Override
    public void cancel(Cancellable event) {
        counters.count(EnforcerCounters.Action.CANCEL);
    }

    @Override
    public void setDamage(EntityDamageEvent event, double damage) {
        counters.count(EnforcerCounters.Action.DAMAGE);
    }

    @Override
    public void setVelocity(PlayerVelocityEvent event, Vector velocity) {
        counters.count(EnforcerCounters.Action.VELOCITY);
    }

    @Override
    public EnforcerCounters counters() {
        return counters;
    }
}
