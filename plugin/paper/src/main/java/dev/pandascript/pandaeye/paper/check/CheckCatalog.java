package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.protocol.v1.Category;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import static dev.pandascript.pandaeye.protocol.v1.Category.CATEGORY_INVALID_PROTOCOL;
import static dev.pandascript.pandaeye.protocol.v1.Category.CATEGORY_INVENTORY;
import static dev.pandascript.pandaeye.protocol.v1.Category.CATEGORY_MOVEMENT;
import static dev.pandascript.pandaeye.protocol.v1.Category.CATEGORY_TICK_SPEED;
import static dev.pandascript.pandaeye.protocol.v1.Category.CATEGORY_WORLD_INTERACTION;

/**
 * The local checks of CHECKS.md section 4 (id, category, points, confidence, half-life, cap, certain) plus
 * their local VL decay and setback VL from section 1. {@code CheckCatalogTest} parses the document and
 * fails when the two disagree.
 */
public final class CheckCatalog {

    public static final String TIMER_BALANCE = "timer.balance";
    public static final String MOVEMENT_SPEED = "movement.speed";
    public static final String MOVEMENT_FLY = "movement.fly";
    public static final String MOVEMENT_STEP = "movement.step";
    public static final String MOVEMENT_GROUNDSPOOF = "movement.groundspoof";
    public static final String MOVEMENT_NOSLOW = "movement.noslow";
    public static final String DIG_SPEED = "dig.speed";
    public static final String PROTOCOL_PITCH = "protocol.pitch";
    public static final String PROTOCOL_POSITION = "protocol.position";
    public static final String PROTOCOL_SLOT = "protocol.slot";
    public static final String PROTOCOL_SELFHIT = "protocol.selfhit";
    public static final String PROTOCOL_CURSOR = "protocol.cursor";
    public static final String PROTOCOL_FLOOD = "protocol.flood";
    public static final String PROTOCOL_ABILITIES = "protocol.abilities";
    public static final String INVENTORY_CLOSED = "inventory.closed";

    /** Local VL decay for checks whose section in CHECKS.md does not state one. */
    private static final double DEFAULT_DECAY_SECONDS = 300;

    private static final List<CheckDescriptor> ALL = List.of(
            d(TIMER_BALANCE, CATEGORY_TICK_SPEED, "6", 0.85, 300, 60, false, 120, 2),
            d(MOVEMENT_SPEED, CATEGORY_MOVEMENT, "2..6", 0.75, 120, 50, false, 30, 5),
            d(MOVEMENT_FLY, CATEGORY_MOVEMENT, "4..10", 0.8, 120, 60, false, 30, 3),
            d(MOVEMENT_STEP, CATEGORY_MOVEMENT, "4..8", 0.8, 120, 50, false, 60, 0),
            d(MOVEMENT_GROUNDSPOOF, CATEGORY_MOVEMENT, "3..6", 0.8, 300, 40, false, 60, 0),
            d(MOVEMENT_NOSLOW, CATEGORY_MOVEMENT, "3..6", 0.75, 120, 40, false, 30, 0),
            d(DIG_SPEED, CATEGORY_WORLD_INTERACTION, "3..7", 0.8, 300, 50, false, 60, 0),
            d(PROTOCOL_PITCH, CATEGORY_INVALID_PROTOCOL, "25", 1.0, 3600, 200, true, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_POSITION, CATEGORY_INVALID_PROTOCOL, "25", 1.0, 3600, 200, true, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_SLOT, CATEGORY_INVALID_PROTOCOL, "10 (0 redundant)", 0.95, 600, 20, false, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_SELFHIT, CATEGORY_INVALID_PROTOCOL, "25", 1.0, 3600, 200, true, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_CURSOR, CATEGORY_INVALID_PROTOCOL, "3", 0.6, 600, 15, false, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_FLOOD, CATEGORY_INVALID_PROTOCOL, "3", 0.7, 600, 20, false, DEFAULT_DECAY_SECONDS, 0),
            d(PROTOCOL_ABILITIES, CATEGORY_INVALID_PROTOCOL, "5", 0.9, 600, 30, false, DEFAULT_DECAY_SECONDS, 0),
            d(INVENTORY_CLOSED, CATEGORY_INVENTORY, "3", 0.8, 300, 20, false, DEFAULT_DECAY_SECONDS, 0));

    private static final Map<String, CheckDescriptor> BY_ID = index();

    private CheckCatalog() {
    }

    public static List<CheckDescriptor> all() {
        return ALL;
    }

    public static CheckDescriptor get(String id) {
        CheckDescriptor descriptor = BY_ID.get(id);
        if (descriptor == null) {
            throw new IllegalArgumentException("unknown local check " + id);
        }
        return descriptor;
    }

    public static boolean contains(String id) {
        return BY_ID.containsKey(id);
    }

    private static CheckDescriptor d(String id, Category category, String points, double confidence, int halfLife,
                                     int cap, boolean certain, double decaySeconds, double setbackVl) {
        return new CheckDescriptor(id, category, points, confidence, halfLife, cap, certain, decaySeconds, setbackVl);
    }

    private static Map<String, CheckDescriptor> index() {
        Map<String, CheckDescriptor> map = new LinkedHashMap<>();
        for (CheckDescriptor descriptor : ALL) {
            map.put(descriptor.id(), descriptor);
        }
        return Map.copyOf(map);
    }
}
