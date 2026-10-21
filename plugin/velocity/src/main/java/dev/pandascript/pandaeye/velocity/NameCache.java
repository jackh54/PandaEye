package dev.pandascript.pandaeye.velocity;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;

/**
 * The proxy's offline player cache: names of players seen by this proxy process, newest last, bounded. Velocity
 * has no offline player store of its own; BAN verdicts for players who already left resolve their name here.
 */
final class NameCache {

    static final int MAX = 10_000;

    private final Map<UUID, String> names = new LinkedHashMap<>(256, 0.75f, true) {
        @Override
        protected boolean removeEldestEntry(Map.Entry<UUID, String> eldest) {
            return size() > MAX;
        }
    };

    synchronized void seen(UUID player, String name) {
        names.put(player, name);
    }

    synchronized String get(UUID player) {
        return names.get(player);
    }

    synchronized int size() {
        return names.size();
    }
}
