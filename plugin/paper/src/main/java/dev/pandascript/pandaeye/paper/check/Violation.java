package dev.pandascript.pandaeye.paper.check;

import java.util.Map;

/**
 * One violation of a local check, as it becomes a LocalFlag. The cloud derives points from the check's
 * CHECKS.md formula and {@link #severity()}; the plugin adds {@link #vlIncrement()} to the local VL.
 *
 * @param severity 0..1, how far beyond the limit this one was
 * @param variant  check-specific variant, empty if none (e.g. protocol.slot {@code range} / {@code redundant})
 */
public record Violation(double severity, double vlIncrement, String info, Map<String, Double> data, String variant) {

    public Violation {
        severity = Math.max(0, Math.min(1, severity));
        data = Map.copyOf(data);
        info = info == null ? "" : info;
        variant = variant == null ? "" : variant;
    }

    public static Violation of(double severity, double vlIncrement, String info, Map<String, Double> data) {
        return new Violation(severity, vlIncrement, info, data, "");
    }

    public Violation withVariant(String newVariant) {
        return new Violation(severity, vlIncrement, info, data, newVariant);
    }
}
