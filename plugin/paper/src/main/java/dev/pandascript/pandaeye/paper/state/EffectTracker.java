package dev.pandascript.pandaeye.paper.state;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.TreeMap;

/**
 * Potion effects on the player as (applied, pending) amplifiers; -1 = absent. All effects are kept for
 * telemetry; checks read the movement-relevant ones. Netty thread only.
 */
public final class EffectTracker {

    public static final String SPEED = "speed";
    public static final String SLOWNESS = "slowness";
    public static final String JUMP_BOOST = "jump_boost";
    public static final String LEVITATION = "levitation";
    public static final String SLOW_FALLING = "slow_falling";
    public static final String DOLPHINS_GRACE = "dolphins_grace";

    private static final int MAX_EFFECTS = 64;

    private final Map<String, Tracked<Integer>> effects = new LinkedHashMap<>();
    private boolean known;

    /** Effects are unknown only after a hot load until the baseline arrives. */
    public boolean known() {
        return known;
    }

    void resetEmpty() {
        effects.clear();
        known = true;
    }

    void resetUnknown() {
        effects.clear();
        known = false;
    }

    void reset(Map<String, Integer> amplifiers) {
        effects.clear();
        amplifiers.forEach((key, amplifier) -> effects.put(normalise(key), new Tracked<>(amplifier)));
        known = true;
    }

    void sent(String key, int amplifier, long nanos) {
        String k = normalise(key);
        Tracked<Integer> tracked = effects.get(k);
        if (tracked == null) {
            if (effects.size() >= MAX_EFFECTS) {
                return;
            }
            tracked = new Tracked<>(-1);
            effects.put(k, tracked);
        }
        tracked.sent(amplifier, nanos);
    }

    void promote(AckClock clock) {
        for (Tracked<Integer> value : effects.values()) {
            value.promote(clock);
        }
    }

    /** Highest amplifier in applied or pending state, -1 when absent in both. */
    public int maxAmplifier(String key) {
        Tracked<Integer> tracked = effects.get(key);
        return tracked == null ? -1 : (int) tracked.max(Integer::doubleValue);
    }

    /** Lowest amplifier in applied or pending state, -1 when absent in either. */
    public int minAmplifier(String key) {
        Tracked<Integer> tracked = effects.get(key);
        return tracked == null ? -1 : (int) tracked.min(Integer::doubleValue);
    }

    /** True when the effect is present in the applied or any pending state. */
    public boolean present(String key) {
        return maxAmplifier(key) >= 0;
    }

    /** Effects as the server last sent them, for telemetry. */
    public Map<String, Integer> latest() {
        Map<String, Integer> out = new TreeMap<>();
        effects.forEach((key, value) -> {
            Integer amplifier = value.latest();
            if (amplifier != null && amplifier >= 0) {
                out.put("minecraft:" + key, amplifier);
            }
        });
        return out;
    }

    static String normalise(String key) {
        int colon = key.indexOf(':');
        return colon >= 0 ? key.substring(colon + 1) : key;
    }
}
