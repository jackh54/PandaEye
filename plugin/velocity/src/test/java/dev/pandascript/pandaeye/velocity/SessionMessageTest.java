package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.event.player.ServerPostConnectEvent;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ServerConnection;
import com.velocitypowered.api.proxy.messages.ChannelIdentifier;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.proxy.ProxySessionToken;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import org.junit.jupiter.api.Test;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The {@code pandaeye:session} message (ARCHITECTURE 4.3, 6): exactly 73 bytes, version 1, player UUID,
 * network session UUID, unix seconds (8, big-endian), HMAC-SHA256(session-secret, first 41 bytes); sent to the
 * backend after each connect through {@code player.getCurrentServer()}; accepted by the backend's verifier.
 */
class SessionMessageTest {

    static final Log QUIET = new Log() {
        @Override
        public void info(String message) {
        }

        @Override
        public void warn(String message) {
        }

        @Override
        public void warn(String message, Throwable error) {
            throw new AssertionError(message, error);
        }

        @Override
        public void debug(String message) {
        }
    };

    private static final UUID PLAYER = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");
    private static final String SECRET = "correct horse battery staple";

    static VelocityConfig config(String extra) {
        List<String> warnings = new ArrayList<>();
        VelocityConfig config = VelocityConfig.load("session-secret: \"" + SECRET + "\"\n" + extra, warnings);
        assertEquals(List.of(), warnings);
        return config;
    }

    @Test
    void theLayoutIsExactlyTheDocumentedOne() throws Exception {
        UUID session = UUID.fromString("0f1e2d3c-4b5a-4968-8776-655443322110");
        long seconds = 1_761_000_000L;
        byte[] message = ProxySessionToken.encode(PLAYER, session, seconds, SECRET);
        assertEquals(73, message.length);
        assertEquals(1, message[0]);
        ByteBuffer buffer = ByteBuffer.wrap(message);
        buffer.get();
        assertEquals(PLAYER, new UUID(buffer.getLong(), buffer.getLong()));
        assertEquals(session, new UUID(buffer.getLong(), buffer.getLong()));
        assertEquals(seconds, buffer.getLong());
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(SECRET.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        byte[] expected = mac.doFinal(Arrays.copyOfRange(message, 0, 41));
        assertArrayEquals(expected, Arrays.copyOfRange(message, 41, 73));
        // Big-endian timestamp at bytes 33..40.
        assertArrayEquals(new byte[]{0, 0, 0, 0, 0x68, (byte) 0xf6, (byte) 0xba, 0x40}, Arrays.copyOfRange(message, 33, 41));
    }

    @Test
    void theTrackerSendsItToTheBackendAndTheBackendVerifierAcceptsIt() {
        List<byte[]> sent = new ArrayList<>();
        List<String> channels = new ArrayList<>();
        ServerConnection connection = TestProxies.of(ServerConnection.class, (name, args) -> {
            if (name.equals("sendPluginMessage")) {
                channels.add(((ChannelIdentifier) args[0]).getId());
                sent.add((byte[]) args[1]);
                return true;
            }
            return null;
        });
        Player player = TestProxies.of(Player.class, Map.of("getUniqueId", PLAYER, "getUsername", "Steve",
                "getCurrentServer", Optional.of(connection)));
        long now = System.currentTimeMillis() / 1_000;
        ProxyTracker tracker = new ProxyTracker(event -> { }, EventClock.system(), () -> config(""),
                BedrockCheck.detect(getClass().getClassLoader()), new NameCache(), () -> true, () -> now,
                new RateLimitedLog(QUIET, 1));
        tracker.hotLoad(List.of(player));
        tracker.onServerPostConnect(new ServerPostConnectEvent(player, null));
        assertEquals(List.of("pandaeye:session", "pandaeye:session"), channels, "hot load and post-connect");
        byte[] message = sent.get(1);
        var verified = ProxySessionToken.verify(message, SECRET, PLAYER, now).orElseThrow();
        assertEquals(tracker.session(PLAYER).networkSession, verified.networkSession());
        assertTrue(ProxySessionToken.verify(message, "wrong secret", PLAYER, now).isEmpty());
        assertEquals(2, tracker.sessionMessages());
    }

    @Test
    void nothingIsSentWithoutASecret() {
        List<Object> sent = new ArrayList<>();
        ServerConnection connection = TestProxies.of(ServerConnection.class, (name, args) -> {
            sent.add(name);
            return null;
        });
        Player player = TestProxies.of(Player.class, Map.of("getUniqueId", PLAYER, "getUsername", "Steve",
                "getCurrentServer", Optional.of(connection)));
        VelocityConfig noSecret = VelocityConfig.load("mode: shadow\n", new ArrayList<>());
        ProxyTracker tracker = new ProxyTracker(event -> { }, EventClock.system(), () -> noSecret,
                BedrockCheck.detect(getClass().getClassLoader()), new NameCache(), () -> true, () -> 0,
                new RateLimitedLog(QUIET, 1));
        tracker.hotLoad(List.of(player));
        tracker.onServerPostConnect(new ServerPostConnectEvent(player, null));
        assertTrue(sent.stream().noneMatch("sendPluginMessage"::equals));
    }
}
