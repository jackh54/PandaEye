package dev.pandascript.pandaeye.paper.config;

import dev.pandascript.pandaeye.paper.check.CheckCatalog;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckSettings;
import org.bukkit.configuration.ConfigurationSection;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * Reads config.yml into a {@link PluginConfig}. Invalid values fall back to the safe default with a warning;
 * loading never fails. Check ids contain dots, which Bukkit treats as path separators, so a check's section
 * is looked up by its full path ({@code checks.timer.balance}).
 */
public final class ConfigLoader {

    private final List<String> warnings = new ArrayList<>();

    public List<String> warnings() {
        return List.copyOf(warnings);
    }

    public PluginConfig load(ConfigurationSection root) {
        return new PluginConfig(
                mode(root.getString("mode", "shadow")),
                root.getString("server-name", ""),
                root.getString("network-id", ""),
                new PluginConfig.Cloud(
                        root.getBoolean("cloud.enabled", true),
                        root.getString("cloud.endpoint", "wss://cloud.pandaeye.dev/v1/connect"),
                        root.getString("cloud.license-key", ""),
                        clamp(root, "cloud.batch-interval-ms", 50, 10, 1_000),
                        clamp(root, "cloud.max-buffer-mb", 64, 1, 512),
                        clamp(root, "cloud.max-buffer-age-minutes", 10, 1, 60),
                        root.getBoolean("cloud.allow-insecure", false)),
                new PluginConfig.Telemetry(
                        root.getBoolean("telemetry.full", false),
                        clamp(root, "telemetry.focus-seconds", 5, 1, 60),
                        clampDouble(root, "telemetry.idle-sample-hz", 2, 0.5, 20)),
                checks(root),
                new PluginConfig.Alerts(
                        root.getBoolean("alerts.console", true),
                        inGame(root.getString("alerts.in-game-default", "auto")),
                        root.getBoolean("alerts.log-file", true)),
                new PluginConfig.Exempt(
                        root.getBoolean("exempt.bedrock", true),
                        root.getBoolean("exempt.bypass-detection-in-shadow", false)),
                new PluginConfig.Proxy(
                        root.getBoolean("proxy.session-channel", false),
                        root.getString("proxy.secret", "")),
                root.getBoolean("setbacks.enabled", true),
                new PluginConfig.Punishments(
                        root.getString("punishments.kick-command", ""),
                        root.getString("punishments.kick-message", "Unfair advantage detected"),
                        root.getString("punishments.ban-command",
                                "ban %player% Unfair advantage. Appeal at %appeal_url% with code %appeal_code%"),
                        root.getString("punishments.unban-command", "pardon %player%"),
                        root.getString("punishments.appeal-url", ""),
                        clamp(root, "punishments.max-bans-per-hour", 3, 0, 1_000),
                        clamp(root, "punishments.max-kicks-per-10min", 5, 0, 1_000)),
                root.getBoolean("mitigations.enabled", true),
                root.getBoolean("debug", false));
    }

    private PluginConfig.Mode mode(String value) {
        return switch (value.trim().toLowerCase(Locale.ROOT)) {
            case "shadow" -> PluginConfig.Mode.SHADOW;
            case "live" -> PluginConfig.Mode.LIVE;
            default -> {
                warnings.add("mode '" + value + "' is not shadow or live; using shadow");
                yield PluginConfig.Mode.SHADOW;
            }
        };
    }

    private PluginConfig.InGameAlerts inGame(String value) {
        try {
            return PluginConfig.InGameAlerts.valueOf(value.trim().toUpperCase(Locale.ROOT));
        } catch (IllegalArgumentException e) {
            warnings.add("alerts.in-game-default '" + value + "' is not auto, on or off; using auto");
            return PluginConfig.InGameAlerts.AUTO;
        }
    }

    private Map<String, CheckSettings> checks(ConfigurationSection root) {
        Map<String, CheckSettings> checks = new LinkedHashMap<>();
        for (CheckDescriptor descriptor : CheckCatalog.all()) {
            ConfigurationSection section = root.getConfigurationSection("checks." + descriptor.id());
            if (section == null) {
                checks.put(descriptor.id(), CheckSettings.DEFAULT);
                continue;
            }
            Map<String, Double> thresholds = new LinkedHashMap<>();
            for (String key : section.getKeys(false)) {
                if (key.equals("enabled")) {
                    continue;
                }
                if (section.isDouble(key) || section.isInt(key) || section.isLong(key)) {
                    thresholds.put(key, section.getDouble(key));
                } else {
                    warnings.add("checks." + descriptor.id() + "." + key + " is not a number; ignored");
                }
            }
            checks.put(descriptor.id(), new CheckSettings(section.getBoolean("enabled", true), thresholds));
        }
        ConfigurationSection all = root.getConfigurationSection("checks");
        if (all != null) {
            for (String path : all.getKeys(true)) {
                if (path.endsWith(".enabled") && !CheckCatalog.contains(path.substring(0, path.length() - 8))) {
                    warnings.add("checks." + path + " does not belong to a known local check; ignored");
                }
            }
        }
        return checks;
    }

    private int clamp(ConfigurationSection root, String path, int fallback, int min, int max) {
        int value = root.getInt(path, fallback);
        if (value < min || value > max) {
            warnings.add(path + " must be between " + min + " and " + max + "; using " + fallback);
            return fallback;
        }
        return value;
    }

    private double clampDouble(ConfigurationSection root, String path, double fallback, double min, double max) {
        double value = root.getDouble(path, fallback);
        if (value < min || value > max || Double.isNaN(value)) {
            warnings.add(path + " must be between " + min + " and " + max + "; using " + fallback);
            return fallback;
        }
        return value;
    }
}
