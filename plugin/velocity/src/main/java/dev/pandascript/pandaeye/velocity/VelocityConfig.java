package dev.pandascript.pandaeye.velocity;

import dev.pandascript.pandaeye.common.litebans.LiteBansSettings;
import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;
import org.yaml.snakeyaml.LoaderOptions;
import org.yaml.snakeyaml.Yaml;
import org.yaml.snakeyaml.constructor.SafeConstructor;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/**
 * {@code plugins/pandaeye/config.yml} of the Velocity plugin (ARCHITECTURE 6). Invalid values fall back to the
 * safe default with a warning; loading never fails. Parsed with SnakeYAML's safe constructor (no tags).
 */
public record VelocityConfig(
        boolean live,
        String serverName,
        String networkId,
        String sessionSecret,
        boolean cloudEnabled,
        String endpoint,
        String licenseKey,
        boolean allowInsecure,
        int batchIntervalMs,
        int maxBufferMb,
        int maxBufferAgeMinutes,
        PunishmentSettings punishments,
        LiteBansSettings litebans,
        boolean debug) {

    public static final VelocityConfig DEFAULT = load("", new ArrayList<>());

    public boolean shadow() {
        return !live;
    }

    /** Parses {@code yaml}; problems are added to {@code warnings}. */
    public static VelocityConfig load(String yaml, List<String> warnings) {
        Map<?, ?> root;
        try {
            Object parsed = new Yaml(new SafeConstructor(new LoaderOptions())).load(yaml);
            root = parsed instanceof Map<?, ?> map ? map : Map.of();
        } catch (RuntimeException e) {
            warnings.add("config.yml could not be parsed (" + e.getMessage() + "); using the defaults (shadow mode)");
            root = Map.of();
        }
        Reader r = new Reader(root, warnings);
        String mode = r.string("mode", "shadow").trim().toLowerCase(Locale.ROOT);
        if (!mode.equals("shadow") && !mode.equals("live")) {
            warnings.add("mode '" + mode + "' is not shadow or live; using shadow");
            mode = "shadow";
        }
        return new VelocityConfig(
                mode.equals("live"),
                r.string("server-name", "proxy"),
                r.string("network-id", ""),
                r.string("session-secret", ""),
                r.bool("cloud.enabled", true),
                r.string("cloud.endpoint", "wss://eye.pandascript.dev/v1/connect"),
                r.string("cloud.license-key", ""),
                r.bool("cloud.allow-insecure", false),
                r.integer("cloud.batch-interval-ms", 50, 10, 1_000),
                r.integer("cloud.max-buffer-mb", 64, 1, 512),
                r.integer("cloud.max-buffer-age-minutes", 10, 1, 60),
                new PunishmentSettings(
                        r.string("punishments.kick-command", ""),
                        r.string("punishments.kick-message", "Unfair advantage detected"),
                        r.string("punishments.ban-command", ""),
                        r.string("punishments.unban-command", ""),
                        r.string("punishments.appeal-url", ""),
                        r.integer("punishments.max-bans-per-hour", 3, 0, 1_000),
                        r.integer("punishments.max-kicks-per-10min", 5, 0, 1_000)),
                new LiteBansSettings(
                        r.bool("labels.litebans.enabled", true),
                        r.strings("labels.litebans.cheat-reasons", LiteBansSettings.DEFAULT_CHEAT_REASONS),
                        r.strings("labels.litebans.ignore-reasons", LiteBansSettings.DEFAULT_IGNORE_REASONS),
                        r.strings("labels.litebans.legit-unban-reasons", LiteBansSettings.DEFAULT_LEGIT_UNBAN_REASONS),
                        r.bool("labels.litebans.count-console", false),
                        r.integer("labels.litebans.backfill-days", LiteBansSettings.DEFAULT_BACKFILL_DAYS, 0,
                                LiteBansSettings.MAX_BACKFILL_DAYS)),
                r.bool("debug", false));
    }

    private record Reader(Map<?, ?> root, List<String> warnings) {

        Object get(String path) {
            Object node = root;
            for (String part : path.split("\\.")) {
                if (!(node instanceof Map<?, ?> map)) {
                    return null;
                }
                node = map.get(part);
            }
            return node;
        }

        String string(String path, String fallback) {
            Object value = get(path);
            if (value == null) {
                return fallback;
            }
            if (value instanceof String || value instanceof Number || value instanceof Boolean) {
                return value.toString();
            }
            warnings.add(path + " is not a string; using '" + fallback + "'");
            return fallback;
        }

        boolean bool(String path, boolean fallback) {
            Object value = get(path);
            if (value == null) {
                return fallback;
            }
            if (value instanceof Boolean b) {
                return b;
            }
            warnings.add(path + " is not true or false; using " + fallback);
            return fallback;
        }

        /** A list of strings; a missing key is the default, an explicitly empty list stays empty. */
        List<String> strings(String path, List<String> fallback) {
            Object value = get(path);
            if (value == null) {
                return fallback;
            }
            if (!(value instanceof List<?> list)) {
                warnings.add(path + " is not a list; using the default");
                return fallback;
            }
            List<String> out = new ArrayList<>();
            for (Object item : list) {
                if (item instanceof String || item instanceof Number || item instanceof Boolean) {
                    out.add(item.toString());
                } else if (item != null) {
                    warnings.add(path + " contains an entry that is not a string; ignored");
                }
            }
            return out;
        }

        int integer(String path, int fallback, int min, int max) {
            Object value = get(path);
            if (value == null) {
                return fallback;
            }
            if (value instanceof Number n && n.doubleValue() == n.intValue() && n.intValue() >= min && n.intValue() <= max) {
                return n.intValue();
            }
            warnings.add(path + " must be a whole number between " + min + " and " + max + "; using " + fallback);
            return fallback;
        }
    }
}
