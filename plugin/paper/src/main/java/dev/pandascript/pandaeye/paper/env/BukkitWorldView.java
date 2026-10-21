package dev.pandascript.pandaeye.paper.env;

import dev.pandascript.pandaeye.paper.check.Box;
import dev.pandascript.pandaeye.paper.check.WorldView;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.block.Block;
import org.bukkit.entity.Player;
import org.bukkit.util.BoundingBox;

/** {@link WorldView} over a Bukkit world. Entity (region) thread only. */
public final class BukkitWorldView implements WorldView {

    private static final double FOOTPRINT_HALF = 0.3;
    private static final double BELOW = 0.5000001;
    /** Boxes covering more blocks than this are treated as exempt rather than scanned. */
    private static final int MAX_SCANNED_BLOCKS = 256;
    private static final UseEffects USE_EFFECTS = UseEffects.detect();

    private final World world;
    private final Player player;

    public BukkitWorldView(World world, Player player) {
        this.world = world;
        this.player = player;
    }

    @Override
    public double maxFriction(double x, double y, double z) {
        double max = 0;
        for (Material type : footprint(x, y, z)) {
            max = Math.max(max, friction(type));
        }
        return max;
    }

    @Override
    public double minFriction(double x, double y, double z) {
        double min = Double.POSITIVE_INFINITY;
        for (Material type : footprint(x, y, z)) {
            min = Math.min(min, friction(type));
        }
        return min;
    }

    @Override
    public boolean soulSpeedBlockUnder(double x, double y, double z) {
        for (Material type : footprint(x, y, z)) {
            if (BlockKinds.soulSpeed(type)) {
                return true;
            }
        }
        return false;
    }

    @Override
    public boolean hasCollisions(Box box) {
        return world.hasCollisionsIn(bukkit(box));
    }

    @Override
    public boolean exemptNear(Box box) {
        int minX = floor(box.minX());
        int minY = floor(box.minY());
        int minZ = floor(box.minZ());
        int maxX = floor(box.maxX());
        int maxY = floor(box.maxY());
        int maxZ = floor(box.maxZ());
        long volume = (long) (maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1);
        if (volume > MAX_SCANNED_BLOCKS) {
            return true;
        }
        BoundingBox bukkitBox = bukkit(box);
        for (int bx = minX; bx <= maxX; bx++) {
            for (int by = minY; by <= maxY; by++) {
                for (int bz = minZ; bz <= maxZ; bz++) {
                    Block block = world.getBlockAt(bx, by, bz);
                    Material type = block.getType();
                    if (BlockKinds.liquid(block) || BlockKinds.stuck(type)) {
                        return true;
                    }
                    if (BlockKinds.exempt(type) && (!type.isCollidable() || BlockKinds.collides(block, bukkitBox))) {
                        return true;
                    }
                }
            }
        }
        return collidableEntityNear(box, 0);
    }

    @Override
    public boolean collidableEntityNear(Box box, double radius) {
        BoundingBox search = bukkit(box.expand(radius));
        return !world.getNearbyEntities(search, entity -> entity != player && Collidables.pushesOrCollides(entity))
                .isEmpty();
    }

    @Override
    public double usingItemSpeedMultiplier() {
        if (!player.hasActiveItem()) {
            return Double.NaN;
        }
        return USE_EFFECTS.multiplier(player.getActiveItem());
    }

    private Material[] footprint(double x, double y, double z) {
        int by = floor(y - BELOW);
        return new Material[]{
                world.getBlockAt(floor(x - FOOTPRINT_HALF), by, floor(z - FOOTPRINT_HALF)).getType(),
                world.getBlockAt(floor(x + FOOTPRINT_HALF), by, floor(z - FOOTPRINT_HALF)).getType(),
                world.getBlockAt(floor(x - FOOTPRINT_HALF), by, floor(z + FOOTPRINT_HALF)).getType(),
                world.getBlockAt(floor(x + FOOTPRINT_HALF), by, floor(z + FOOTPRINT_HALF)).getType()};
    }

    @SuppressWarnings("deprecation")
    private static double friction(Material type) {
        return type.isAir() ? 0.6 : type.getSlipperiness();
    }

    private static int floor(double value) {
        return (int) Math.floor(value);
    }

    static BoundingBox bukkit(Box box) {
        return new BoundingBox(box.minX(), box.minY(), box.minZ(), box.maxX(), box.maxY(), box.maxZ());
    }
}
