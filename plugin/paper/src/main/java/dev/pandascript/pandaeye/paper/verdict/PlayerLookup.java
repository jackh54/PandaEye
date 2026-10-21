package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import org.bukkit.Bukkit;
import org.bukkit.entity.Player;

import java.util.UUID;
import java.util.function.Supplier;

/** The players the verdict handler can act on. Called on the cloud thread: thread-safe reads only. */
public interface PlayerLookup {

    /** An online player with a PandaEye session. {@code bypass} is null until evaluated on the entity thread. */
    record Online(Player player, String name, Boolean bypass, Session session) {
    }

    /** Null when the player has no open session here. */
    Online online(UUID player);

    /** The server's offline player cache (usercache / player data), null when unknown. */
    String cachedName(UUID player);

    /** Online state from the session registry; offline names from {@code Bukkit.getOfflinePlayer}. */
    static PlayerLookup sessions(Supplier<SessionRegistry> registry) {
        return new PlayerLookup() {
            @Override
            public Online online(UUID id) {
                SessionRegistry sessions = registry.get();
                Session session = sessions == null ? null : sessions.get(id);
                if (session == null || session.closed()) {
                    return null;
                }
                Player player = session.player();
                return player == null ? null : new Online(player, session.name(), session.signals().bypass(), session);
            }

            @Override
            public String cachedName(UUID id) {
                try {
                    return Bukkit.getOfflinePlayer(id).getName();
                } catch (Throwable t) {
                    return null;
                }
            }
        };
    }
}
