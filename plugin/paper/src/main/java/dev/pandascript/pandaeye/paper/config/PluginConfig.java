package dev.pandascript.pandaeye.paper.config;

import dev.pandascript.pandaeye.common.litebans.LiteBansSettings;
import dev.pandascript.pandaeye.paper.check.CheckSettings;

import java.util.Map;

/** The validated contents of config.yml (ARCHITECTURE 4.11). Immutable; replaced as a whole on reload. */
public record PluginConfig(
        Mode mode,
        String serverName,
        String networkId,
        Cloud cloud,
        Telemetry telemetry,
        Map<String, CheckSettings> checks,
        Alerts alerts,
        Exempt exempt,
        Proxy proxy,
        boolean setbacks,
        Punishments punishments,
        boolean mitigations,
        LiteBansSettings litebans,
        boolean debug) {

    public PluginConfig {
        checks = Map.copyOf(checks);
    }

    public enum Mode {
        /** Observe only: never sends, cancels or modifies packets, never punishes or mitigates. */
        SHADOW,
        /** Enforcement allowed when the dashboard also enables enforcement. */
        LIVE
    }

    public enum InGameAlerts {
        AUTO,
        ON,
        OFF
    }

    public record Cloud(boolean enabled, String endpoint, String licenseKey, int batchIntervalMs, int maxBufferMb,
                        int maxBufferAgeMinutes, boolean allowInsecure) {
    }

    public record Telemetry(boolean full, int focusSeconds, double idleSampleHz) {
    }

    /** {@code logAllPeaks}: a peaks-log row for every player and check, not only those with candidates. */
    public record Alerts(boolean console, InGameAlerts inGameDefault, boolean logFile, boolean logAllPeaks) {
    }

    public record Exempt(boolean bedrock, boolean bypassDetectionInShadow) {
    }

    public record Proxy(boolean sessionChannel, String secret) {
    }

    public record Punishments(String kickCommand, String kickMessage, String banCommand, String unbanCommand,
                              String appealUrl, int maxBansPerHour, int maxKicksPer10Min) {
    }

    public boolean shadow() {
        return mode == Mode.SHADOW;
    }

    public CheckSettings check(String id) {
        return checks.getOrDefault(id, CheckSettings.DEFAULT);
    }

    /** Whether staff get in-game alerts by default (auto = on in live mode, off in shadow mode). */
    public boolean inGameAlertsByDefault() {
        return switch (alerts.inGameDefault()) {
            case ON -> true;
            case OFF -> false;
            case AUTO -> mode == Mode.LIVE;
        };
    }
}
