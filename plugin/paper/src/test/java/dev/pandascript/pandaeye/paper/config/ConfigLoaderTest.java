package dev.pandascript.pandaeye.paper.config;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import org.bukkit.configuration.file.YamlConfiguration;
import org.junit.jupiter.api.Test;

import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ConfigLoaderTest {

    @Test
    void theShippedConfigIsTheDocumentedDefault() throws Exception {
        YamlConfiguration yaml;
        try (var reader = new InputStreamReader(getClass().getResourceAsStream("/config.yml"), StandardCharsets.UTF_8)) {
            yaml = YamlConfiguration.loadConfiguration(reader);
        }
        ConfigLoader loader = new ConfigLoader();
        PluginConfig config = loader.load(yaml);
        assertEquals(java.util.List.of(), loader.warnings());
        assertEquals(PluginConfig.Mode.SHADOW, config.mode());
        assertEquals("wss://cloud.pandaeye.dev/v1/connect", config.cloud().endpoint());
        assertEquals(50, config.cloud().batchIntervalMs());
        assertEquals(64, config.cloud().maxBufferMb());
        assertEquals(10, config.cloud().maxBufferAgeMinutes());
        assertFalse(config.cloud().allowInsecure());
        assertEquals(5, config.telemetry().focusSeconds());
        assertEquals(2, config.telemetry().idleSampleHz());
        assertEquals(PluginConfig.InGameAlerts.AUTO, config.alerts().inGameDefault());
        assertFalse(config.inGameAlertsByDefault(), "auto = off in shadow mode");
        assertTrue(config.exempt().bedrock());
        assertFalse(config.exempt().bypassDetectionInShadow());
        assertFalse(config.proxy().sessionChannel());
        assertEquals("pardon %player%", config.punishments().unbanCommand());
        assertEquals(3, config.punishments().maxBansPerHour());
        assertEquals(5, config.punishments().maxKicksPer10Min());
        for (CheckDescriptor check : CheckCatalog.all()) {
            assertTrue(yaml.isConfigurationSection("checks." + check.id()), check.id() + " listed in config.yml");
            assertTrue(config.check(check.id()).enabled());
        }
    }

    @Test
    void invalidValuesFallBackToSafeDefaultsWithWarnings() throws Exception {
        YamlConfiguration yaml = new YamlConfiguration();
        yaml.loadFromString("""
                mode: enforce
                cloud:
                  batch-interval-ms: 0
                  max-buffer-mb: 100000
                alerts:
                  in-game-default: sometimes
                telemetry:
                  idle-sample-hz: 500
                """);
        ConfigLoader loader = new ConfigLoader();
        PluginConfig config = loader.load(yaml);
        assertEquals(PluginConfig.Mode.SHADOW, config.mode());
        assertEquals(50, config.cloud().batchIntervalMs());
        assertEquals(64, config.cloud().maxBufferMb());
        assertEquals(PluginConfig.InGameAlerts.AUTO, config.alerts().inGameDefault());
        assertEquals(2, config.telemetry().idleSampleHz());
        assertEquals(5, loader.warnings().size(), loader.warnings().toString());
    }

    @Test
    void checkSectionsCarryThresholdsAndUnknownChecksAreReported() throws Exception {
        YamlConfiguration yaml = new YamlConfiguration();
        yaml.loadFromString("""
                mode: live
                alerts:
                  in-game-default: auto
                checks:
                  movement.speed: { enabled: false, buffer: 4, single-excess: 0.6, note: hi }
                  reach.distance: { enabled: true }
                """);
        ConfigLoader loader = new ConfigLoader();
        PluginConfig config = loader.load(yaml);
        assertFalse(config.check(CheckCatalog.MOVEMENT_SPEED).enabled());
        assertEquals(4, config.check(CheckCatalog.MOVEMENT_SPEED).threshold("buffer", 3));
        assertEquals(0.6, config.check(CheckCatalog.MOVEMENT_SPEED).threshold("single-excess", 0.5));
        assertTrue(config.check(CheckCatalog.TIMER_BALANCE).enabled());
        assertTrue(config.inGameAlertsByDefault(), "auto = on in live mode");
        assertTrue(loader.warnings().stream().anyMatch(w -> w.contains("note")));
        assertTrue(loader.warnings().stream().anyMatch(w -> w.contains("reach.distance")));
    }
}
