package dev.pandascript.pandaeye.velocity;

import dev.pandascript.pandaeye.common.litebans.LiteBansSettings;
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
        assertEquals(LiteBansSettings.DEFAULT, config.litebans());
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

    @Test
    void liteBansLabelsAreConfigurable() {
        List<String> warnings = new ArrayList<>();
        LiteBansSettings settings = VelocityConfig.load("""
                labels:
                  litebans:
                    enabled: false
                    cheat-reasons: [Ghost Client, triggerbot]
                    ignore-reasons: []
                    count-console: true
                    backfill-days: 7
                """, warnings).litebans();
        assertEquals(List.of(), warnings);
        assertFalse(settings.enabled());
        assertEquals(List.of("ghost client", "triggerbot"), settings.cheatReasons());
        assertEquals(List.of(), settings.ignoreReasons(), "an empty list stays empty");
        assertEquals(LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS, settings.legitUnbanReasons());
        assertTrue(settings.countConsole());
        assertEquals(7, settings.backfillDays());

        List<String> invalid = new ArrayList<>();
        assertEquals(LiteBansSettings.DEFAULT, VelocityConfig.load("""
                labels:
                  litebans:
                    cheat-reasons: hack
                    legit-unban-reasons: [false ban, {nested: map}, falsely, wrongful, mistake, appeal accepted, false positive,
                      wrongly, wrong ban, accepted appeal, appeal approved]
                    backfill-days: 1000
                """, invalid).litebans());
        assertEquals(3, invalid.size(), invalid.toString());
    }
}
