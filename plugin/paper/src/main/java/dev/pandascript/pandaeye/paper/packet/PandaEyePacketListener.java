package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.event.PacketListener;
import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.event.UserDisconnectEvent;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.packettype.PacketTypeCommon;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.wrapper.configuration.client.WrapperConfigClientPluginMessage;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerJoinGame;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import dev.pandascript.pandaeye.protocol.v1.GameMode;

import java.util.function.Supplier;

/**
 * PandaEye's single PacketEvents listener, registered at MONITOR. Observes only: never cancels, never
 * modifies, never leaves a wrapper attached (see {@link Wrappers}). Server-bound packets are processed even
 * if another plugin cancelled them (the client still sent them); cancelled client-bound packets are ignored
 * (they were never sent). Every callback goes through the {@link ActiveGate} and catches every Throwable.
 */
public final class PandaEyePacketListener implements PacketListener {

    private final ActiveGate gate;
    private final SessionRegistry sessions;
    private final ClientPacketReader client;
    private final ServerPacketReader server = new ServerPacketReader();
    private final PacketCost cost;
    private final RateLimitedLog errors;

    public PandaEyePacketListener(ActiveGate gate, SessionRegistry sessions, Supplier<PluginConfig> config,
                                  ProxySessionListener proxySessions, PacketCost cost,
                                  RateLimitedLog errors) {
        this.gate = gate;
        this.sessions = sessions;
        this.client = new ClientPacketReader(config, proxySessions);
        this.cost = cost;
        this.errors = errors;
    }

    @Override
    public void onPacketReceive(PacketReceiveEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            long now = System.nanoTime();
            PacketTypeCommon type = event.getPacketType();
            if (type instanceof PacketType.Play.Client play) {
                Session session = sessions.get(event.getUser());
                if (session != null) {
                    session.beforePacket(now);
                    client.handle(event, play, session, now);
                    cost.record(System.nanoTime() - now);
                }
            } else if (type == PacketType.Configuration.Client.PLUGIN_MESSAGE) {
                WrapperConfigClientPluginMessage message = Wrappers.read(event, WrapperConfigClientPluginMessage.class,
                        WrapperConfigClientPluginMessage::new);
                if (PluginMessages.BRAND.equals(message.getChannelName())) {
                    sessions.configurationBrand(event.getUser(), PluginMessages.brand(message.getData()));
                }
            }
        } catch (Throwable t) {
            errors.warn("receive " + safeName(event.getPacketType()), "packet handling failed", t);
        } finally {
            gate.exit();
        }
    }

    @Override
    public void onPacketSend(PacketSendEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            if (event.isCancelled() || !(event.getPacketType() instanceof PacketType.Play.Server play)) {
                return;
            }
            long now = System.nanoTime();
            if (play == PacketType.Play.Server.JOIN_GAME) {
                joinGame(event, now);
                return;
            }
            Session session = sessions.get(event.getUser());
            if (session != null) {
                session.beforePacket(now);
                server.handle(event, play, session, now);
                cost.record(System.nanoTime() - now);
            }
        } catch (Throwable t) {
            errors.warn("send " + safeName(event.getPacketType()), "packet handling failed", t);
        } finally {
            gate.exit();
        }
    }

    @Override
    public void onUserDisconnect(UserDisconnectEvent event) {
        if (!gate.enter()) {
            return;
        }
        try {
            sessions.disconnected(event.getUser());
        } catch (Throwable t) {
            errors.warn("disconnect", "session cleanup failed", t);
        } finally {
            gate.exit();
        }
    }

    private void joinGame(PacketSendEvent event, long now) {
        User user = event.getUser();
        if (user.getUUID() == null) {
            return;
        }
        int entityId;
        GameMode mode;
        String world;
        try {
            WrapperPlayServerJoinGame join = Wrappers.read(event, WrapperPlayServerJoinGame.class,
                    WrapperPlayServerJoinGame::new);
            entityId = join.getEntityId();
            mode = GameModes.of(join.getGameMode());
            world = join.getWorldName();
        } catch (RuntimeException e) {
            // A JOIN_GAME PacketEvents cannot decode still yields the entity id; the game mode stays unknown.
            errors.warn("join game", "could not decode JOIN_GAME; game mode unknown until it changes", e);
            entityId = RawPeek.leadingInt(event.getByteBuf());
            mode = null;
            world = null;
        }
        sessions.joinGame(user, ClientInfos.of(user), entityId, mode, world, now);
        cost.record(System.nanoTime() - now);
    }

    private static String safeName(PacketTypeCommon type) {
        return type == null ? "unknown" : type.getName();
    }
}
