package dev.pandascript.pandaeye.paper.log;

import org.bukkit.entity.Player;

import java.util.Collection;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Staff who receive in-game alerts: holders of {@code pandaeye.alerts} with alerts toggled on. Maintained on
 * join, quit and toggle so alerts never scan permissions or look players up. A toggle overrides the default.
 */
public final class AlertRecipients {

    private final ConcurrentHashMap<UUID, Player> recipients = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<UUID, Boolean> toggles = new ConcurrentHashMap<>();

    /** Join (entity thread): {@code permitted} is {@code hasPermission("pandaeye.alerts")}. */
    public void joined(Player player, boolean permitted, boolean onByDefault) {
        if (permitted && toggles.getOrDefault(player.getUniqueId(), onByDefault)) {
            recipients.put(player.getUniqueId(), player);
        } else {
            recipients.remove(player.getUniqueId());
        }
    }

    public void quit(UUID player) {
        recipients.remove(player);
    }

    /** {@code on == null} flips the current state. Returns the new state. */
    public boolean toggle(Player player, Boolean on) {
        UUID id = player.getUniqueId();
        boolean enabled = on != null ? on : !recipients.containsKey(id);
        toggles.put(id, enabled);
        if (enabled) {
            recipients.put(id, player);
        } else {
            recipients.remove(id);
        }
        return enabled;
    }

    public Collection<Player> all() {
        return List.copyOf(recipients.values());
    }

    public int size() {
        return recipients.size();
    }

    public void clear() {
        recipients.clear();
        toggles.clear();
    }
}
