package dev.pandascript.pandaeye.paper.check;

import java.util.Map;

/**
 * {@code checks.<id>} from config.yml: the enabled switch and optional threshold overrides. Checks read
 * thresholds with a code default, so the shipped config only needs {@code enabled}.
 */
public record CheckSettings(boolean enabled, Map<String, Double> thresholds) {

    public static final CheckSettings DEFAULT = new CheckSettings(true, Map.of());

    public CheckSettings {
        thresholds = Map.copyOf(thresholds);
    }

    public double threshold(String key, double fallback) {
        Double value = thresholds.get(key);
        return value == null ? fallback : value;
    }
}
