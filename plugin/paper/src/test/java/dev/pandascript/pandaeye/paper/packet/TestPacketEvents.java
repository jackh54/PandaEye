package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.PacketEvents;
import com.github.retrooper.packetevents.PacketEventsAPI;
import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.injector.ChannelInjector;
import com.github.retrooper.packetevents.manager.player.PlayerManager;
import com.github.retrooper.packetevents.manager.protocol.ProtocolManager;
import com.github.retrooper.packetevents.manager.server.ServerManager;
import com.github.retrooper.packetevents.manager.server.ServerVersion;
import com.github.retrooper.packetevents.netty.NettyManager;
import com.github.retrooper.packetevents.protocol.ConnectionState;
import com.github.retrooper.packetevents.protocol.packettype.PacketTypeCommon;
import com.github.retrooper.packetevents.protocol.player.ClientVersion;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.protocol.player.UserProfile;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;
import io.github.retrooper.packetevents.impl.netty.NettyManagerImpl;
import io.netty.buffer.ByteBuf;
import io.netty.buffer.Unpooled;

import java.util.UUID;

/**
 * A minimal PacketEvents runtime for unit tests: the real event, wrapper and netty buffer classes with a
 * stub API (server 1.21.11, {@code reEncodeByDefault = true} as shipped), so tests can build real
 * PacketSendEvent/PacketReceiveEvent objects from wrapper-encoded buffers.
 */
final class TestPacketEvents {

    static final ServerVersion SERVER = ServerVersion.V_1_21_11;
    static final ClientVersion CLIENT = ClientVersion.V_1_21_11;

    private TestPacketEvents() {
    }

    static synchronized void install() {
        if (PacketEvents.getAPI() != null) {
            return;
        }
        NettyManager netty = new NettyManagerImpl();
        ServerManager server = () -> SERVER;
        PacketEventsAPI<Object> api = new PacketEventsAPI<>() {
            @Override
            public boolean isLoaded() {
                return true;
            }

            @Override
            public void init() {
            }

            @Override
            public boolean isInitialized() {
                return true;
            }

            @Override
            public boolean isTerminated() {
                return false;
            }

            @Override
            public Object getPlugin() {
                return null;
            }

            @Override
            public ServerManager getServerManager() {
                return server;
            }

            @Override
            public ProtocolManager getProtocolManager() {
                return null;
            }

            @Override
            public PlayerManager getPlayerManager() {
                return null;
            }

            @Override
            public NettyManager getNettyManager() {
                return netty;
            }

            @Override
            public ChannelInjector getInjector() {
                return null;
            }
        };
        api.getSettings().reEncodeByDefault(true);
        PacketEvents.setAPI(api);
    }

    static User user(UUID uuid) {
        User user = new User(new Object(), ConnectionState.PLAY, CLIENT, new UserProfile(uuid, "Tester"));
        user.setEncoderState(ConnectionState.PLAY);
        user.setDecoderState(ConnectionState.PLAY);
        return user;
    }

    /** The wrapper's payload (no packet id), as PacketEvents hands it to listeners. */
    static ByteBuf encode(PacketWrapper<?> wrapper) {
        ByteBuf buffer = Unpooled.buffer();
        wrapper.setBuffer(buffer);
        wrapper.write();
        return buffer;
    }

    static PacketSendEvent send(User user, PacketTypeCommon type, ByteBuf payload) {
        return new PacketSendEvent(type.getId(CLIENT), type, SERVER, user.getChannel(), user, null, payload) {
        };
    }

    static PacketReceiveEvent receive(User user, PacketTypeCommon type, ByteBuf payload) {
        return new PacketReceiveEvent(type.getId(CLIENT), type, SERVER, user.getChannel(), user, null, payload) {
        };
    }
}
