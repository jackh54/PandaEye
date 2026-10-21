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
        assertEquals(ProxySessionToken.LENGTH, token.length);
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
        token[20] ^= 1;
        assertTrue(ProxySessionToken.verify(token, "secret", player, 1_000).isEmpty());
        assertTrue(ProxySessionToken.verify(new byte[10], "secret", player, 1_000).isEmpty());
        assertThrows(IllegalArgumentException.class, () -> ProxySessionToken.encode(player, session, 1, ""));
    }
}
