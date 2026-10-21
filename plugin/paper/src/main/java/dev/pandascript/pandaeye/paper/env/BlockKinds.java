package dev.pandascript.pandaeye.paper.env;

import org.bukkit.Material;
import org.bukkit.Tag;
import org.bukkit.block.Block;
import org.bukkit.block.data.BlockData;
import org.bukkit.block.data.Waterlogged;
import org.bukkit.util.BoundingBox;

/** Block classifications used by the environment sampler and the confirmation step. */
public final class BlockKinds {

    private BlockKinds() {
    }

    public static boolean ice(Material type) {
        return type == Material.ICE || type == Material.PACKED_ICE || type == Material.BLUE_ICE
                || type == Material.FROSTED_ICE;
    }

    public static boolean soulSpeed(Material type) {
        return type == Material.SOUL_SAND || type == Material.SOUL_SOIL;
    }

    /** Cobweb, sweet berry bush and powder snow apply the stuck-in-block multiplier. */
    public static boolean stuck(Material type) {
        return type == Material.COBWEB || type == Material.SWEET_BERRY_BUSH || type == Material.POWDER_SNOW;
    }

    public static boolean liquid(Block block) {
        Material type = block.getType();
        if (type == Material.WATER || type == Material.LAVA || type == Material.BUBBLE_COLUMN) {
            return true;
        }
        BlockData data = block.getBlockData();
        return data instanceof Waterlogged waterlogged && waterlogged.isWaterlogged();
    }

    /** CHECKS 1.1 exemptNear block types (liquids are tested separately with {@link #liquid}). */
    public static boolean exempt(Material type) {
        return stuck(type) || Tag.CLIMBABLE.isTagged(type) || Tag.BEDS.isTagged(type)
                || type == Material.HONEY_BLOCK || type == Material.SLIME_BLOCK
                || type == Material.PISTON || type == Material.STICKY_PISTON || type == Material.PISTON_HEAD
                || type == Material.MOVING_PISTON;
    }

    /**
     * True when the block's collision shape intersects the world-space box. Collision shapes are block-local,
     * so the box is shifted into the block's coordinates first.
     */
    public static boolean collides(Block block, BoundingBox box) {
        return block.getCollisionShape().overlaps(box.clone().shift(-block.getX(), -block.getY(), -block.getZ()));
    }
}
