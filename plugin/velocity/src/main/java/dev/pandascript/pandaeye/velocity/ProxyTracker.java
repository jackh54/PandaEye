package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.event.PostOrder;
import com.velocitypowered.api.event.Subscribe;
import com.velocitypowered.api.event.connection.DisconnectEvent;
import com.velocitypowered.api.event.connection.PluginMessageEvent;
import com.velocitypowered.api.event.connection.PostLoginEvent;
import com.velocitypowered.api.event.player.PlayerClientBrandEvent;
import com.velocitypowered.api.event.player.ServerConnectedEvent;
import com.velocitypowered.api.event.player.ServerPostConnectEvent;
import com.velocitypowered.api.network.ProtocolVersion;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ServerConnection;
import com.velocitypowered.api.proxy.messages.ChannelIdentifier;
import com.velocitypowered.api.proxy.messages.MinecraftChannelIdentifier;
import com.velocitypowered.api.proxy.server.RegisteredServer;
import dev.pandascript.pandaeye.common.proxy.ProxySessionToken;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.PlayerJoin;
import dev.pandascript.pandaeye.protocol.v1.PlayerQuit;
import dev.pandascript.pandaeye.protocol.v1.ServerSwitch;

import java.util.Collection;
import java.util.List;
import java.util.Locale;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.LongAdder;
import java.util.function.BooleanSupplier;
import java.util.function.Consumer;
import java.util.function.LongSupplier;
import java.util.function.Supplier;

/**
 * The proxy's player tracking (ARCHITECTURE 6): PlayerJoin on PostLoginEvent (with a random network session),
 * PlayerJoin{update} for the client brand and every 60 s, ServerSwitch on ServerConnectedEvent, PlayerQuit on
 * DisconnectEvent; after each backend connect the signed {@code pandaeye:session} message to that backend; and
 * client-originated messages on that channel are dropped. Velocity runs these handlers on its event threads;
 * each is cheap, non-blocking and catches everything. Read-only towards players: it never sends anything to a
 * client and never changes an event other than the private channel's messages.
 */
@SuppressWarnings("deprecation") // Subscribe.order: PostOrder works on every Velocity 3.x; priority needs 3.3+
public final class ProxyTracker {

    static final ChannelIdentifier SESSION_CHANNEL = MinecraftChannelIdentifier.from(ProxySessionToken.CHANNEL);
    static final int MAX_BRAND = 64;
    private static final int MAX_PENDING_BRANDS = 10_000;

    /** One player's proxy session. */
    static final class State {
        final UUID player;
        final String name;
        final UUID networkSession;
        final int protocol;
        final String clientVersion;
        final boolean bedrock;
        final boolean hotLoaded;
        final boolean exempt;
        volatile String brand = "";
        volatile String server = "";

        State(UUID player, String name, UUID networkSession, int protocol, String clientVersion, boolean bedrock,
              boolean hotLoaded, boolean exempt) {
            this.player = player;
            this.name = name;
            this.networkSession = networkSession;
            this.protocol = protocol;
            this.clientVersion = clientVersion;
            this.bedrock = bedrock;
            this.hotLoaded = hotLoaded;
            this.exempt = exempt;
        }
    }

    private final ConcurrentHashMap<UUID, State> sessions = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<UUID, String> pendingBrands = new ConcurrentHashMap<>();
    private final Consumer<Event> events;
    private final EventClock clock;
    private final Supplier<VelocityConfig> config;
    private final BedrockCheck bedrock;
    private final NameCache names;
    private final BooleanSupplier active;
    private final LongSupplier unixSeconds;
    private final RateLimitedLog errors;
    private final LongAdder sessionMessages = new LongAdder();
    private final LongAdder droppedMessages = new LongAdder();

    /** {@code events} is the transport's event sink ({@code null} when the cloud is off). */
    ProxyTracker(Consumer<Event> events, EventClock clock, Supplier<VelocityConfig> config, BedrockCheck bedrock, NameCache names,
                 BooleanSupplier active, LongSupplier unixSeconds, RateLimitedLog errors) {
        this.events = events;
        this.clock = clock;
        this.config = config;
        this.bedrock = bedrock;
        this.names = names;
        this.active = active;
        this.unixSeconds = unixSeconds;
        this.errors = errors;
    }

    // ---------------------------------------------------------------------------------------------
    // Events
    // ---------------------------------------------------------------------------------------------

    @Subscribe(order = PostOrder.LAST)
    public void onPostLogin(PostLoginEvent event) {
        if (!active.getAsBoolean()) {
            return;
        }
        try {
            started(event.getPlayer(), false);
        } catch (Throwable t) {
            errors.warn("login", "PostLoginEvent handling failed", t);
        }
    }

    @Subscribe(order = PostOrder.LAST)
    public void onBrand(PlayerClientBrandEvent event) {
        if (!active.getAsBoolean()) {
            return;
        }
        try {
            String brand = cleanBrand(event.getBrand());
            UUID id = event.getPlayer().getUniqueId();
            State state = sessions.get(id);
            if (state == null) {
                if (pendingBrands.size() < MAX_PENDING_BRANDS) {
                    pendingBrands.putIfAbsent(id, brand);
                }
                return;
            }
            if (state.brand.isEmpty() && !brand.isEmpty()) {
                state.brand = brand;
                offer(join(state, true));
            }
        } catch (Throwable t) {
            errors.warn("brand", "PlayerClientBrandEvent handling failed", t);
        }
    }

    @Subscribe(order = PostOrder.LAST)
    public void onServerConnected(ServerConnectedEvent event) {
        if (!active.getAsBoolean()) {
            return;
        }
        try {
            State state = sessions.get(event.getPlayer().getUniqueId());
            if (state == null) {
                return;
            }
            String from = event.getPreviousServer().map(ProxyTracker::name).orElse("");
            String to = name(event.getServer());
            state.server = to;
            offer(event(state.player).setServerSwitch(ServerSwitch.newBuilder().setFromServer(from).setToServer(to)
                    .setNetworkSession(state.networkSession.toString())).build());
        } catch (Throwable t) {
            errors.warn("switch", "ServerConnectedEvent handling failed", t);
        }
    }

    @Subscribe(order = PostOrder.LAST)
    public void onServerPostConnect(ServerPostConnectEvent event) {
        if (!active.getAsBoolean()) {
            return;
        }
        try {
            sendSession(event.getPlayer());
        } catch (Throwable t) {
            errors.warn("session", "could not send the pandaeye:session message", t);
        }
    }

    /**
     * Messages on {@code pandaeye:session} never come from a client or a backend legitimately (the proxy sends
     * its own directly to the backend), so they are dropped; this keeps a client from forging one. Not gated on
     * the plugin being active.
     */
    @Subscribe(order = PostOrder.LAST)
    public void onPluginMessage(PluginMessageEvent event) {
        try {
            if (!SESSION_CHANNEL.getId().equals(event.getIdentifier().getId())) {
                return;
            }
            if (event.getSource() instanceof Player || event.getSource() instanceof ServerConnection) {
                event.setResult(PluginMessageEvent.ForwardResult.handled());
                droppedMessages.increment();
            }
        } catch (Throwable t) {
            errors.warn("message", "PluginMessageEvent handling failed", t);
        }
    }

    @Subscribe(order = PostOrder.LAST)
    public void onDisconnect(DisconnectEvent event) {
        try {
            UUID id = event.getPlayer().getUniqueId();
            pendingBrands.remove(id);
            State state = sessions.remove(id);
            if (state != null && active.getAsBoolean()) {
                DisconnectEvent.LoginStatus status = event.getLoginStatus();
                offer(event(state.player).setQuit(PlayerQuit.newBuilder()
                        .setReason(status == null ? "disconnected" : status.name().toLowerCase(Locale.ROOT))).build());
            }
        } catch (Throwable t) {
            errors.warn("quit", "DisconnectEvent handling failed", t);
        }
    }

    // ---------------------------------------------------------------------------------------------

    /** Enable: players already online get a session (hot_loaded) and a session message to their backend. */
    void hotLoad(Collection<Player> online) {
        for (Player player : online) {
            try {
                State state = started(player, true);
                state.server = player.getCurrentServer().map(c -> name(c.getServer())).orElse("");
                sendSession(player);
            } catch (Throwable t) {
                errors.warn("hotload", "could not track an online player", t);
            }
        }
    }

    /** Every 60 s: PlayerJoin{update=true} for every session, so a restarted worker recovers session info. */
    void announce() {
        if (!active.getAsBoolean()) {
            return;
        }
        for (State state : List.copyOf(sessions.values())) {
            offer(join(state, true));
        }
    }

    int size() {
        return sessions.size();
    }

    long sessionMessages() {
        return sessionMessages.sum();
    }

    long droppedMessages() {
        return droppedMessages.sum();
    }

    State session(UUID player) {
        return sessions.get(player);
    }

    void clear() {
        sessions.clear();
        pendingBrands.clear();
    }

    private State started(Player player, boolean hotLoaded) {
        UUID id = player.getUniqueId();
        ProtocolVersion version = player.getProtocolVersion();
        int protocol = version == null || version.isUnknown() ? -1 : version.getProtocol();
        String clientVersion = version == null || version.isUnknown() ? "" : version.getMostRecentSupportedVersion();
        State state = new State(id, player.getUsername(), UUID.randomUUID(), protocol, clientVersion,
                bedrock.isBedrock(id), hotLoaded, player.hasPermission("pandaeye.bypass"));
        String brand = pendingBrands.remove(id);
        if (brand == null) {
            brand = cleanBrand(player.getClientBrand());
        }
        state.brand = brand;
        sessions.put(id, state);
        names.seen(id, player.getUsername());
        offer(join(state, false));
        return state;
    }

    private void sendSession(Player player) {
        String secret = config.get().sessionSecret();
        State state = sessions.get(player.getUniqueId());
        if (secret.isEmpty() || state == null) {
            return;
        }
        byte[] message = ProxySessionToken.encode(state.player, state.networkSession, unixSeconds.getAsLong(), secret);
        player.getCurrentServer().ifPresent(connection -> {
            connection.sendPluginMessage(SESSION_CHANNEL, message);
            sessionMessages.increment();
        });
    }

    private Event join(State state, boolean update) {
        return event(state.player).setJoin(PlayerJoin.newBuilder()
                .setName(state.name)
                .setProtocolVersion(state.protocol)
                .setClientVersion(state.clientVersion)
                .setBrand(state.brand)
                .setBedrock(state.bedrock)
                .setHotLoaded(state.hotLoaded)
                .setExempt(state.exempt)
                .setNetworkSession(state.networkSession.toString())
                .setUpdate(update)
                .setSession(state.networkSession.toString())).build();
    }

    private Event.Builder event(UUID player) {
        return Event.newBuilder().setPlayer(Uuids.toByteString(player)).setTimeUs(clock.nowMicros());
    }

    private void offer(Event event) {
        if (events != null) {
            events.accept(event);
        }
    }

    private static String name(RegisteredServer server) {
        return server == null || server.getServerInfo() == null ? "" : server.getServerInfo().getName();
    }

    /** First brand only (by the caller), at most 64 characters, control characters removed. */
    static String cleanBrand(String brand) {
        if (brand == null) {
            return "";
        }
        StringBuilder out = new StringBuilder(Math.min(brand.length(), MAX_BRAND));
        for (int i = 0; i < brand.length() && out.length() < MAX_BRAND; i++) {
            char c = brand.charAt(i);
            if (!Character.isISOControl(c)) {
                out.append(c);
            }
        }
        return out.toString();
    }
}
