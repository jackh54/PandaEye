package dev.pandascript.pandaeye.paper.session;

import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.bukkit.entity.Player;

import java.util.Collection;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * All sessions, keyed by PacketEvents {@link User} identity (User does not override equals) and by player UUID.
 * Sessions are created on S JOIN_GAME or during enable, and removed with {@code remove(key, session)} on
 * PlayerQuitEvent, UserDisconnectEvent and the periodic sweep. Thread-safe.
 */
public final class SessionRegistry {

    private static final int MAX_PENDING_BRANDS = 10_000;

    /** Platform reactions to session lifecycle (scheduling, telemetry). */
    public interface Lifecycle {

        /** The Bukkit player of a session is known (join event or hot load). */
        void attached(Session session, Player player);

        void closed(Session session, String reason);
    }

    private final ConcurrentHashMap<User, Session> byUser = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<UUID, Session> byUuid = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<UUID, Player> awaitingPlayers = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<User, String> configurationBrands = new ConcurrentHashMap<>();
    private final EntityIndex entities;
    private final SessionFactory factory;
    private final Lifecycle lifecycle;

    public SessionRegistry(EntityIndex entities, SessionFactory factory, Lifecycle lifecycle) {
        this.entities = entities;
        this.factory = factory;
        this.lifecycle = lifecycle;
    }

    public Session get(User user) {
        return byUser.get(user);
    }

    public Session get(UUID player) {
        return byUuid.get(player);
    }

    public Collection<Session> all() {
        return List.copyOf(byUser.values());
    }

    public int size() {
        return byUser.size();
    }

    public EntityIndex entities() {
        return entities;
    }

    /**
     * Netty thread, S JOIN_GAME: a new session for a new player, or a reset of the existing one when the
     * player re-enters PLAY from the configuration phase.
     */
    public Session joinGame(User user, ClientInfo client, int entityId, GameMode mode, String world, long nanos) {
        Session session = byUser.get(user);
        if (session == null) {
            UUID uuid = user.getUUID();
            Session created = factory.create(user, uuid, user.getName(), client, false, nanos);
            Session raced = byUser.putIfAbsent(user, created);
            if (raced != null) {
                // a hot load created it meanwhile: keep that one (reset by the joinGame below)
                created.close();
                session = raced;
            } else {
                session = created;
                Session replaced = byUuid.put(uuid, session);
                if (replaced != null && replaced != session) {
                    remove(replaced, "replaced by a new connection");
                    byUuid.put(uuid, session);
                }
            }
        }
        session.processor().joinGame(entityId, mode, world, configurationBrands.remove(user), nanos);
        entities.put(entityId, session);
        Player waiting = awaitingPlayers.remove(session.uuid());
        if (waiting != null) {
            lifecycle.attached(session, waiting);
        }
        return session;
    }

    /**
     * Enable: a session for a player who was online before the plugin (unseeded until its snapshot). Returns
     * null, creating nothing, when the player already has a session: a JOIN_GAME seen between the listener
     * registration and this call wins, so there is never a second seed or environment sampler.
     */
    public Session hotLoad(User user, ClientInfo client, Player player, long nanos) {
        UUID uuid = player.getUniqueId();
        if (byUser.containsKey(user) || byUuid.containsKey(uuid)) {
            return null;
        }
        Session session = factory.create(user, uuid, player.getName(), client, true, nanos);
        if (byUser.putIfAbsent(user, session) != null) {
            session.close();
            return null;
        }
        if (byUuid.putIfAbsent(uuid, session) != null) {
            byUser.remove(user, session);
            session.close();
            return null;
        }
        lifecycle.attached(session, player);
        return session;
    }

    /** Bukkit PlayerJoinEvent: attaches the player now, or as soon as its JOIN_GAME is seen. */
    public void playerJoined(Player player) {
        UUID uuid = player.getUniqueId();
        awaitingPlayers.put(uuid, player);
        Session session = byUuid.get(uuid);
        if (session != null && awaitingPlayers.remove(uuid, player)) {
            lifecycle.attached(session, player);
        }
    }

    /** A brand sent during the configuration phase, before the session exists. */
    public void configurationBrand(User user, String brand) {
        if (configurationBrands.size() < MAX_PENDING_BRANDS) {
            configurationBrands.putIfAbsent(user, brand);
        }
    }

    public void quit(UUID player, String reason) {
        awaitingPlayers.remove(player);
        Session session = byUuid.get(player);
        if (session != null) {
            remove(session, reason);
        }
    }

    public void disconnected(User user) {
        configurationBrands.remove(user);
        Session session = byUser.get(user);
        if (session != null) {
            remove(session, "disconnected");
        }
    }

    public void remove(Session session, String reason) {
        byUser.remove(session.user(), session);
        byUuid.remove(session.uuid(), session);
        entities.removeAll(session);
        if (session.close()) {
            lifecycle.closed(session, reason);
        }
    }

    /** Disable: forget everything without lifecycle callbacks. */
    public void clear() {
        for (Session session : byUser.values()) {
            session.close();
        }
        byUser.clear();
        byUuid.clear();
        awaitingPlayers.clear();
        configurationBrands.clear();
        entities.clear();
    }
}
