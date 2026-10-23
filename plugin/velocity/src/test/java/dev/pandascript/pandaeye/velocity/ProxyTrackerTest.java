package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.event.connection.DisconnectEvent;
import com.velocitypowered.api.event.connection.PluginMessageEvent;
import com.velocitypowered.api.event.connection.PostLoginEvent;
import com.velocitypowered.api.event.player.PlayerClientBrandEvent;
import com.velocitypowered.api.event.player.ServerConnectedEvent;
import com.velocitypowered.api.network.ProtocolVersion;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ServerConnection;
import com.velocitypowered.api.proxy.messages.ChannelMessageSink;
import com.velocitypowered.api.proxy.messages.MinecraftChannelIdentifier;
import com.velocitypowered.api.proxy.server.RegisteredServer;
import com.velocitypowered.api.proxy.server.ServerInfo;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Event;
import org.junit.jupiter.api.Test;

import java.net.InetSocketAddress;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Proxy events (ARCHITECTURE 6) and dropping client-originated pandaeye:session messages. */
class ProxyTrackerTest {

    private static final UUID PLAYER = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");

    private final List<Event> events = new ArrayList<>();
    private final AtomicBoolean active = new AtomicBoolean(true);
    private final NameCache names = new NameCache();
    private final ProxyTracker tracker = new ProxyTracker(events::add, EventClock.system(),
            () -> SessionMessageTest.config(""), BedrockCheck.detect(getClass().getClassLoader()), names, active::get,
            () -> System.currentTimeMillis() / 1_000, new RateLimitedLog(SessionMessageTest.QUIET, 1));

    private static Player player(UUID id, String name) {
        return TestProxies.of(Player.class, Map.of("getUniqueId", id, "getUsername", name,
                "getProtocolVersion", ProtocolVersion.MAXIMUM_VERSION));
    }

    private static RegisteredServer server(String name) {
        ServerInfo info = new ServerInfo(name, InetSocketAddress.createUnresolved("localhost", 25565));
        return TestProxies.of(RegisteredServer.class, Map.of("getServerInfo", info));
    }

    @Test
    void aSessionProducesJoinBrandSwitchAndQuit() {
        Player player = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(player));
        tracker.onBrand(new PlayerClientBrandEvent(player, "fabric\u0000\n" + "x".repeat(100)));
        tracker.onBrand(new PlayerClientBrandEvent(player, "vanilla"));
        tracker.onServerConnected(new ServerConnectedEvent(player, server("hub"), null));
        tracker.onServerConnected(new ServerConnectedEvent(player, server("survival"), server("hub")));
        tracker.onDisconnect(new DisconnectEvent(player, DisconnectEvent.LoginStatus.SUCCESSFUL_LOGIN));
        assertEquals(5, events.size(), events.toString());

        Event join = events.get(0);
        assertEquals(PLAYER, Uuids.fromByteString(join.getPlayer()).orElseThrow());
        assertEquals("Steve", join.getJoin().getName());
        assertFalse(join.getJoin().getUpdate());
        assertEquals(ProtocolVersion.MAXIMUM_VERSION.getProtocol(), join.getJoin().getProtocolVersion());
        String networkSession = join.getJoin().getNetworkSession();
        assertTrue(Uuids.isCanonical(networkSession), networkSession);
        assertFalse(join.getJoin().getBedrock());

        Event brand = events.get(1);
        assertTrue(brand.getJoin().getUpdate());
        assertEquals("fabric" + "x".repeat(58), brand.getJoin().getBrand(), "control characters stripped, 64 chars");
        assertEquals(networkSession, brand.getJoin().getNetworkSession());

        assertEquals("", events.get(2).getServerSwitch().getFromServer());
        assertEquals("hub", events.get(2).getServerSwitch().getToServer());
        assertEquals("hub", events.get(3).getServerSwitch().getFromServer());
        assertEquals("survival", events.get(3).getServerSwitch().getToServer());
        assertEquals(networkSession, events.get(3).getServerSwitch().getNetworkSession());
        assertEquals("successful_login", events.get(4).getQuit().getReason());
        assertEquals(0, tracker.size());
        assertEquals("Steve", names.get(PLAYER), "the name stays in the offline cache");
    }

    @Test
    void eachLoginGetsANewNetworkSessionAndBedrockIsDetected() {
        Player first = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(first));
        tracker.onDisconnect(new DisconnectEvent(first, DisconnectEvent.LoginStatus.SUCCESSFUL_LOGIN));
        tracker.onPostLogin(new PostLoginEvent(player(PLAYER, "Steve")));
        assertFalse(events.get(0).getJoin().getNetworkSession().equals(events.get(2).getJoin().getNetworkSession()));
        UUID bedrock = new UUID(0, 0x1234L);
        tracker.onPostLogin(new PostLoginEvent(player(bedrock, ".BedrockGuy")));
        assertTrue(events.get(3).getJoin().getBedrock(), "UUID msb == 0");
    }

    @Test
    void aRejectedDuplicateLoginLeavesTheOnlinePlayersSession() {
        Player online = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(online));
        tracker.onServerConnected(new ServerConnectedEvent(online, server("hub"), null));
        String networkSession = events.get(0).getJoin().getNetworkSession();

        // A second login with the same UUID is refused before PostLoginEvent; Velocity still fires its disconnect.
        Player duplicate = player(PLAYER, "Steve");
        tracker.onDisconnect(new DisconnectEvent(duplicate, DisconnectEvent.LoginStatus.CONFLICTING_LOGIN));
        assertEquals(2, events.size(), "no quit for the player who is still online: " + events);
        assertEquals(1, tracker.size());
        assertSame(online, tracker.session(PLAYER).connection);

        tracker.onServerConnected(new ServerConnectedEvent(online, server("survival"), server("hub")));
        assertEquals(networkSession, events.get(2).getServerSwitch().getNetworkSession(), "switches still tracked");
        tracker.onDisconnect(new DisconnectEvent(online, DisconnectEvent.LoginStatus.SUCCESSFUL_LOGIN));
        assertEquals("successful_login", events.get(3).getQuit().getReason());
        assertEquals(0, tracker.size());
    }

    @Test
    void underKickExistingPlayersTheOldConnectionsLateDisconnectKeepsTheNewSession() {
        Player old = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(old));
        Player replacement = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(replacement));
        String newSession = events.get(1).getJoin().getNetworkSession();
        assertFalse(newSession.equals(events.get(0).getJoin().getNetworkSession()));

        tracker.onDisconnect(new DisconnectEvent(old, DisconnectEvent.LoginStatus.CONFLICTING_LOGIN));
        assertEquals(2, events.size(), "the replaced connection's disconnect is not the player's quit: " + events);
        assertSame(replacement, tracker.session(PLAYER).connection);
        assertEquals(newSession, tracker.session(PLAYER).networkSession.toString());

        tracker.onDisconnect(new DisconnectEvent(replacement, DisconnectEvent.LoginStatus.SUCCESSFUL_LOGIN));
        assertTrue(events.get(2).hasQuit());
        assertEquals(0, tracker.size());
    }

    @Test
    void underKickExistingPlayersAnEarlyDisconnectOfTheOldConnectionIsAQuit() {
        Player old = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(old));
        tracker.onDisconnect(new DisconnectEvent(old, DisconnectEvent.LoginStatus.CONFLICTING_LOGIN));
        Player replacement = player(PLAYER, "Steve");
        tracker.onPostLogin(new PostLoginEvent(replacement));
        assertTrue(events.get(1).hasQuit());
        assertTrue(events.get(2).hasJoin());
        assertSame(replacement, tracker.session(PLAYER).connection);
    }

    @Test
    void periodicAnnouncesAreUpdates() {
        tracker.onPostLogin(new PostLoginEvent(player(PLAYER, "Steve")));
        tracker.announce();
        assertEquals(2, events.size());
        assertTrue(events.get(1).getJoin().getUpdate());
        active.set(false);
        tracker.announce();
        assertEquals(2, events.size(), "nothing after disable");
    }

    @Test
    void clientMessagesOnTheSessionChannelAreDropped() {
        Player player = player(PLAYER, "Steve");
        ServerConnection backend = TestProxies.of(ServerConnection.class, Map.of());
        PluginMessageEvent forged = new PluginMessageEvent(player, backend, ProxyTracker.SESSION_CHANNEL, new byte[73]);
        tracker.onPluginMessage(forged);
        assertFalse(forged.getResult().isAllowed(), "a client must not be able to forge the proxy's message");

        PluginMessageEvent fromBackend = new PluginMessageEvent(backend, player, ProxyTracker.SESSION_CHANNEL, new byte[1]);
        tracker.onPluginMessage(fromBackend);
        assertFalse(fromBackend.getResult().isAllowed());

        PluginMessageEvent other = new PluginMessageEvent(player, backend, MinecraftChannelIdentifier.from("minecraft:brand"),
                new byte[1]);
        tracker.onPluginMessage(other);
        assertTrue(other.getResult().isAllowed(), "other channels are untouched");

        active.set(false);
        PluginMessageEvent afterDisable = new PluginMessageEvent(player, TestProxies.of(ChannelMessageSink.class, Map.of()),
                MinecraftChannelIdentifier.from("pandaeye:session"), new byte[73]);
        tracker.onPluginMessage(afterDisable);
        assertFalse(afterDisable.getResult().isAllowed(), "dropping does not depend on the plugin being active");
        assertEquals(3, tracker.droppedMessages());
    }

    @Test
    void aBrandBeforeTheLoginIsKept() {
        Player player = player(PLAYER, "Steve");
        tracker.onBrand(new PlayerClientBrandEvent(player, "lunarclient"));
        tracker.onPostLogin(new PostLoginEvent(player));
        assertEquals("lunarclient", events.get(0).getJoin().getBrand());
    }
}
