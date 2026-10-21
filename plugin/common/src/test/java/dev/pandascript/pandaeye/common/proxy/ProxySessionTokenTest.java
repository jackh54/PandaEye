package dev.pandascript.pandaeye.common.proxy;

import org.junit.jupiter.api.Test;

import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ProxySessionTokenTest {

    private final UUID player = UUID.randomUUID();
    private final UUID session = UUID.randomUUID();

    @Test
    void verifiesAFreshTokenForThePlayer() {
        byte[] token = ProxySessionToken.encode(player, session, 1_000, "secret");
        assertEquals(73, token.length);
        assertEquals(1, token[0]);
        var verified = ProxySessionToken.verify(token, "secret", player, 1_010).orElseThrow();
        assertEquals(session, verified.networkSession());
    }

    @Test
    void rejectsWrongSecretPlayerTimestampAndTampering() {
        byte[] token = ProxySessionToken.encode(player, session, 1_000, "secret");
        assertTrue(ProxySessionToken.verify(token, "other", player, 1_000).isEmpty());
        assertTrue(ProxySessionToken.verify(token, "", player, 1_000).isEmpty());
        assertTrue(ProxySessionToken.verify(token, "secret", UUID.randomUUID(), 1_000).isEmpty());
        assertTrue(ProxySessionToken.verify(token, "secret", player, 1_031).isEmpty());
        assertTrue(ProxySessionToken.verify(token, "secret", player, 969).isEmpty());
        byte[] versioned = token.clone();
        versioned[0] = 2;
        assertTrue(ProxySessionToken.verify(versioned, "secret", player, 1_000).isEmpty());
        token[20] ^= 1;
        assertTrue(ProxySessionToken.verify(token, "secret", player, 1_000).isEmpty());
        assertTrue(ProxySessionToken.verify(new byte[10], "secret", player, 1_000).isEmpty());
        assertThrows(IllegalArgumentException.class, () -> ProxySessionToken.encode(player, session, 1, ""));
    }
}
