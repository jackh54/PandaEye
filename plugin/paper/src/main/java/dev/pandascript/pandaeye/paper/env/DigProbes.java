package dev.pandascript.pandaeye.paper.env;

import dev.pandascript.pandaeye.paper.state.DigProbe;
import org.bukkit.attribute.Attribute;
import org.bukkit.attribute.AttributeInstance;
import org.bukkit.block.Block;
import org.bukkit.entity.Player;
import org.bukkit.potion.PotionEffect;
import org.bukkit.potion.PotionEffectType;

/** Builds the {@link DigProbe} of a dig START from authoritative state. Region (entity) thread only; read-only. */
public final class DigProbes {

    private DigProbes() {
    }

    public static DigProbe take(Player player, Block block, boolean instaBreak, long nanos) {
        return new DigProbe(block.getX(), block.getY(), block.getZ(), block.getBreakSpeed(player), instaBreak,
                player.isOnGround(), player.isUnderWater(),
                Math.max(amplifier(player, PotionEffectType.HASTE), amplifier(player, PotionEffectType.CONDUIT_POWER)),
                amplifier(player, PotionEffectType.MINING_FATIGUE),
                value(player, Attribute.MINING_EFFICIENCY, 0), value(player, Attribute.BLOCK_BREAK_SPEED, 1),
                value(player, Attribute.SUBMERGED_MINING_SPEED, 0.2), nanos);
    }

    private static int amplifier(Player player, PotionEffectType type) {
        PotionEffect effect = player.getPotionEffect(type);
        return effect == null ? -1 : effect.getAmplifier();
    }

    private static double value(Player player, Attribute attribute, double fallback) {
        AttributeInstance instance = player.getAttribute(attribute);
        return instance == null ? fallback : instance.getValue();
    }
}
