package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.protocol.v1.GameMode;

import java.util.Map;
import java.util.UUID;

/**
 * The hot-load snapshot of a player who was online before the plugin was enabled, taken on the player's
 * entity thread from authoritative server state and applied by the netty thread on the next packet.
 * Attribute values exclude the {@code minecraft:sprinting} modifier; effects map keys to amplifiers.
 */
public record Baseline(
        int entityId,
        GameMode gameMode,
        Abilities abilities,
        Map<String, Double> attributes,
        Map<String, Integer> effects,
        boolean inVehicle,
        int vehicleId,
        boolean gliding,
        boolean riptiding,
        boolean dead,
        int heldSlot,
        String brand,
        boolean bypass,
        UUID worldUid) {

    public Baseline {
        attributes = Map.copyOf(attributes);
        effects = Map.copyOf(effects);
    }
}
