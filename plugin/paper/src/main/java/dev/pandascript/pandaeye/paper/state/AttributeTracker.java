package dev.pandascript.pandaeye.paper.state;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * The attributes local checks need, as (applied, pending) pairs. Values exclude the {@code minecraft:sprinting}
 * modifier; checks add sprint themselves. Netty thread only.
 */
public final class AttributeTracker {

    public static final String MOVEMENT_SPEED = "movement_speed";
    public static final String JUMP_STRENGTH = "jump_strength";
    public static final String GRAVITY = "gravity";
    public static final String STEP_HEIGHT = "step_height";
    public static final String SCALE = "scale";
    public static final String ENTITY_REACH = "entity_interaction_range";
    public static final String BLOCK_REACH = "block_interaction_range";

    /** Vanilla player defaults, which a freshly joined client starts with. */
    public static final Map<String, Double> DEFAULTS = Map.of(
            MOVEMENT_SPEED, 0.1,
            JUMP_STRENGTH, 0.42,
            GRAVITY, 0.08,
            STEP_HEIGHT, 0.6,
            SCALE, 1.0,
            ENTITY_REACH, 3.0,
            BLOCK_REACH, 4.5);

    private final Map<String, Tracked<Double>> values = new LinkedHashMap<>();

    AttributeTracker() {
        for (String key : DEFAULTS.keySet()) {
            values.put(key, new Tracked<>(null));
        }
    }

    /** Normalises {@code minecraft:generic.movement_speed} etc. to the keys above; null if not tracked. */
    public static String normalise(String key) {
        String k = key;
        int colon = k.indexOf(':');
        if (colon >= 0) {
            k = k.substring(colon + 1);
        }
        if (k.startsWith("generic.") || k.startsWith("player.")) {
            k = k.substring(k.indexOf('.') + 1);
        }
        return DEFAULTS.containsKey(k) ? k : null;
    }

    void resetToDefaults() {
        for (Map.Entry<String, Tracked<Double>> entry : values.entrySet()) {
            entry.getValue().reset(DEFAULTS.get(entry.getKey()));
        }
    }

    void resetUnknown() {
        for (Tracked<Double> value : values.values()) {
            value.reset(null);
        }
    }

    /** Baseline values; keys missing from {@code snapshot} fall back to the defaults. */
    void reset(Map<String, Double> snapshot) {
        for (Map.Entry<String, Tracked<Double>> entry : values.entrySet()) {
            entry.getValue().reset(snapshot.getOrDefault(entry.getKey(), DEFAULTS.get(entry.getKey())));
        }
    }

    void sent(String key, double value, long nanos) {
        Tracked<Double> tracked = values.get(key);
        if (tracked != null) {
            tracked.sent(value, nanos);
        }
    }

    void promote(AckClock clock) {
        for (Tracked<Double> value : values.values()) {
            value.promote(clock);
        }
    }

    public Tracked<Double> get(String key) {
        return values.get(key);
    }

    /** max(applied, pending); NaN while unknown. */
    public double lenientMax(String key) {
        return values.get(key).max(Double::doubleValue);
    }

    public boolean known() {
        for (Tracked<Double> value : values.values()) {
            if (!value.known()) {
                return false;
            }
        }
        return true;
    }

    /** The newest values the server sent, for StateChange telemetry (NaN when unknown). */
    public double latest(String key) {
        Double value = values.get(key).latest();
        return value == null ? Double.NaN : value;
    }
}
