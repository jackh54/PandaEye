package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.AttributeMath;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.Baseline;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import org.bukkit.attribute.Attribute;
import org.bukkit.attribute.AttributeInstance;
import org.bukkit.attribute.AttributeModifier;
import org.bukkit.entity.Entity;
import org.bukkit.entity.Player;
import org.bukkit.potion.PotionEffect;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/** Takes the hot-load snapshot of a player from authoritative server state. Entity thread only. */
public final class BaselineSnapshot {

    private static final Map<String, Attribute> ATTRIBUTES = Map.of(
            AttributeTracker.MOVEMENT_SPEED, Attribute.MOVEMENT_SPEED,
            AttributeTracker.JUMP_STRENGTH, Attribute.JUMP_STRENGTH,
            AttributeTracker.GRAVITY, Attribute.GRAVITY,
            AttributeTracker.STEP_HEIGHT, Attribute.STEP_HEIGHT,
            AttributeTracker.SCALE, Attribute.SCALE,
            AttributeTracker.ENTITY_REACH, Attribute.ENTITY_INTERACTION_RANGE,
            AttributeTracker.BLOCK_REACH, Attribute.BLOCK_INTERACTION_RANGE);

    private BaselineSnapshot() {
    }

    public static Baseline take(Player player) {
        Map<String, Double> attributes = new HashMap<>();
        ATTRIBUTES.forEach((key, attribute) -> {
            AttributeInstance instance = player.getAttribute(attribute);
            if (instance != null) {
                attributes.put(key, withoutSprint(instance));
            }
        });
        Map<String, Integer> effects = new HashMap<>();
        for (PotionEffect effect : player.getActivePotionEffects()) {
            effects.put(effect.getType().getKey().getKey(), effect.getAmplifier());
        }
        GameMode mode = gameMode(player.getGameMode());
        Entity vehicle = player.getVehicle();
        return new Baseline(
                player.getEntityId(),
                mode,
                new Abilities(player.isFlying(), player.getAllowFlight(), mode == GameMode.GAME_MODE_CREATIVE,
                        player.getFlySpeed() / 2),
                attributes,
                effects,
                player.isInsideVehicle(),
                vehicle == null ? -1 : vehicle.getEntityId(),
                player.isGliding(),
                player.isRiptiding(),
                player.isDead(),
                player.getInventory().getHeldItemSlot(),
                player.getClientBrandName(),
                player.hasPermission("pandaeye.bypass"),
                player.getWorld().getUID());
    }

    /** The attribute value without the {@code minecraft:sprinting} modifier. */
    static double withoutSprint(AttributeInstance instance) {
        List<AttributeMath.Modifier> modifiers = new ArrayList<>();
        for (AttributeModifier modifier : instance.getModifiers()) {
            if (AttributeMath.SPRINTING_MODIFIER.equals(modifier.getKey().toString())) {
                continue;
            }
            AttributeMath.Operation operation = switch (modifier.getOperation()) {
                case ADD_NUMBER -> AttributeMath.Operation.ADD_VALUE;
                case ADD_SCALAR -> AttributeMath.Operation.ADD_MULTIPLIED_BASE;
                case MULTIPLY_SCALAR_1 -> AttributeMath.Operation.ADD_MULTIPLIED_TOTAL;
            };
            modifiers.add(new AttributeMath.Modifier(operation, modifier.getAmount()));
        }
        return AttributeMath.value(instance.getBaseValue(), modifiers);
    }

    public static GameMode gameMode(org.bukkit.GameMode mode) {
        return switch (mode) {
            case CREATIVE -> GameMode.GAME_MODE_CREATIVE;
            case ADVENTURE -> GameMode.GAME_MODE_ADVENTURE;
            case SPECTATOR -> GameMode.GAME_MODE_SPECTATOR;
            case SURVIVAL -> GameMode.GAME_MODE_SURVIVAL;
        };
    }
}
