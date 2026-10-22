package dev.pandascript.pandaeye.velocity;

import org.junit.jupiter.api.Test;

import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class VelocityConfigTest {

    @Test
    void theShippedConfigIsShadowWithoutProxyPunishments() throws Exception {
        String yaml;
        try (InputStream in = getClass().getResourceAsStream("/config.yml")) {
            yaml = new String(in.readAllBytes(), StandardCharsets.UTF_8);
        }
        List<String> warnings = new ArrayList<>();
        VelocityConfig config = VelocityConfig.load(yaml, warnings);
        assertEquals(List.of(), warnings);
        assertTrue(config.shadow());
        assertEquals("", config.sessionSecret());
        assertEquals("wss://eye.pandascript.dev/v1/connect", config.endpoint());
        assertFalse(config.allowInsecure());
        assertEquals("", config.punishments().banCommand());
        assertEquals("", config.punishments().kickCommand());
        assertEquals(3, config.punishments().maxBansPerHour());
        assertEquals(5, config.punishments().maxKicksPer10Min());
        assertEquals(config, VelocityConfig.DEFAULT);
    }

    @Test
    void invalidValuesFallBackToSafeDefaults() {
        List<String> warnings = new ArrayList<>();
        VelocityConfig config = VelocityConfig.load("""
                mode: enforce
                cloud:
                  batch-interval-ms: 0
                  allow-insecure: "yes"
                punishments:
                  max-bans-per-hour: -1
                """, warnings);
        assertTrue(config.shadow());
        assertEquals(50, config.batchIntervalMs());
        assertFalse(config.allowInsecure());
        assertEquals(3, config.punishments().maxBansPerHour());
        assertEquals(4, warnings.size(), warnings.toString());
        List<String> broken = new ArrayList<>();
        assertTrue(VelocityConfig.load("mode: [live\n", broken).shadow());
        assertEquals(1, broken.size());
        assertTrue(VelocityConfig.load("!!javax.script.ScriptEngineManager [!!java.net.URLClassLoader [[]]]", broken)
                .shadow(), "no arbitrary types");
    }
}
