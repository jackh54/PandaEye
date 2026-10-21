package dev.pandascript.pandaeye.common.transport;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class EndpointPolicyTest {

    @Test
    void acceptsWssAndLoopbackWs() {
        assertEquals(EndpointPolicy.Kind.SECURE, EndpointPolicy.evaluate("wss://cloud.example/v1/connect", false).kind());
        for (String host : new String[]{"127.0.0.1", "127.8.9.10", "localhost", "[::1]"}) {
            assertEquals(EndpointPolicy.Kind.INSECURE_LOOPBACK,
                    EndpointPolicy.evaluate("ws://" + host + ":8080/v1/connect", false).kind(), host);
        }
    }

    @Test
    void refusesPlainWsToOtherHostsUnlessAllowed() {
        EndpointPolicy.Result refused = EndpointPolicy.evaluate("ws://10.0.0.5:8080/v1/connect", false);
        assertFalse(refused.usable());
        assertTrue(refused.message().contains("cloud.allow-insecure"));
        EndpointPolicy.Result allowed = EndpointPolicy.evaluate("ws://10.0.0.5:8080/v1/connect", true);
        assertEquals(EndpointPolicy.Kind.INSECURE_ALLOWED, allowed.kind());
        assertEquals(EndpointPolicy.INSECURE_WARNING, allowed.message());
        assertFalse(EndpointPolicy.evaluate("ws://localhost.evil.example/v1/connect", false).usable());
    }

    @Test
    void refusesOtherSchemesAndGarbage() {
        assertFalse(EndpointPolicy.evaluate("https://cloud.example/v1/connect", true).usable());
        assertFalse(EndpointPolicy.evaluate("not a uri", true).usable());
        assertFalse(EndpointPolicy.evaluate("", true).usable());
        assertFalse(EndpointPolicy.evaluate(null, true).usable());
    }
}
