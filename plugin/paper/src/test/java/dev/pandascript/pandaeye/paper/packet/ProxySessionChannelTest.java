package dev.pandascript.pandaeye.paper.packet;

import dev.pandascript.pandaeye.common.proxy.ProxySessionToken;
import dev.pandascript.pandaeye.common.proxy.ProxySessionToken.Rejection;
import dev.pandascript.pandaeye.paper.config.ConfigLoader;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionSignals;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import org.bukkit.configuration.InvalidConfigurationException;
import org.bukkit.configuration.file.YamlConfiguration;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;

/**
 * The backend side of {@code pandaeye:session}: accepted once per connection, and every rejected message counted
 * by reason for /pandaeye status (a wrong or empty secret or clock skew must not fail silently).
 */
class ProxySessionChannelTest {

    private static final String SECRET = "correct horse battery staple";
    private static final ClientInfo CLIENT = new ClientInfo(774, "1.21.11", true, true, true);

    private final UUID player = UUID.randomUUID();
    private final UUID networkSession = UUID.randomUUID();
    private final List<Session> accepted = new ArrayList<>();
    private final ProxySessionCounters counters = new ProxySessionCounters();
    private PluginConfig config;

    private static PluginConfig config(boolean sessionChannel, String secret) throws InvalidConfigurationException {
        YamlConfiguration yaml = new YamlConfiguration();
        yaml.loadFromString("proxy:\n  session-channel: " + sessionChannel + "\n  secret: \"" + secret + "\"\n");
        return new ConfigLoader().load(yaml);
    }

    private ProxySessionChannel channel() {
        return new ProxySessionChannel(() -> config, (session, nanos) -> accepted.add(session), counters);
    }

    private Session session() {
        return new Session(null, player, "Steve", false, CLIENT, new SessionSignals(false, () -> config,
                (from, to) -> false), System.nanoTime());
    }

    private byte[] token(UUID forPlayer, long unixSeconds, String secret) {
        return ProxySessionToken.encode(forPlayer, networkSession, unixSeconds, secret);
    }

    private static long now() {
        return System.currentTimeMillis() / 1_000;
    }

    @Test
    void aValidMessageIsAcceptedOncePerConnection() throws Exception {
        config = config(true, SECRET);
        ProxySessionChannel channel = channel();
        Session session = session();
        channel.received(player, session, token(player, now(), SECRET), 1);
        channel.received(player, session, token(player, now(), SECRET), 2);
        assertEquals(List.of(session), accepted);
        assertEquals(networkSession.toString(), session.networkSession());
        assertEquals(1, counters.accepted());
        assertEquals(0, counters.rejectedTotal(), "a repeat on the same connection is not a rejection");
    }

    @Test
    void rejectedMessagesAreCountedByReason() throws Exception {
        config = config(true, SECRET);
        ProxySessionChannel channel = channel();
        channel.received(player, session(), token(player, now(), "the proxy's different secret"), 1);
        channel.received(player, session(), token(player, now() - 31, SECRET), 1);
        channel.received(player, session(), token(player, now() + 120, SECRET), 1);
        channel.received(player, session(), token(UUID.randomUUID(), now(), SECRET), 1);
        channel.received(player, session(), new byte[ProxySessionToken.LENGTH], 1);
        assertEquals(List.of(), accepted);
        assertEquals(0, counters.accepted());
        assertEquals(1, counters.rejected(Rejection.WRONG_SECRET));
        assertEquals(2, counters.rejected(Rejection.CLOCK_SKEW));
        assertEquals(1, counters.rejected(Rejection.WRONG_PLAYER));
        assertEquals(1, counters.rejected(Rejection.MALFORMED));
        assertEquals(0, counters.rejected(Rejection.NO_SECRET));
        assertEquals(5, counters.rejectedTotal());
    }

    @Test
    void aMessageBufferedBeforeTheSessionIsCheckedAndCountedWhenItStarts() throws Exception {
        config = config(true, "");
        ProxySessionChannel channel = channel();
        channel.received(player, null, token(player, now(), SECRET), 1_000);
        assertEquals(0, counters.rejectedTotal(), "buffered (configuration phase), not checked yet");
        channel.sessionStarted(session(), 2_000);
        assertEquals(1, counters.rejected(Rejection.NO_SECRET), "session-channel on without proxy.secret");
        assertEquals(List.of(), accepted);
    }

    @Test
    void nothingIsCheckedOrCountedWhileTheChannelIsOff() throws Exception {
        config = config(false, SECRET);
        channel().received(player, session(), token(player, now(), SECRET), 1);
        channel().received(player, session(), token(player, now(), "other"), 1);
        assertEquals(List.of(), accepted);
        assertEquals(0, counters.accepted());
        assertEquals(0, counters.rejectedTotal());
    }
}
