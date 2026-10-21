package dev.pandascript.pandaeye.paper.env;

import dev.pandascript.pandaeye.paper.check.Box;
import dev.pandascript.pandaeye.paper.check.WorldView;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.block.Block;
import org.bukkit.entity.Player;
import org.bukkit.util.BoundingBox;

/**
 * {@link WorldView} over a Bukkit world. Entity (region) thread only. Never reads a chunk that is not
 * {@link ChunkAccess readable}: such a query answers the exempt/lenient way and marks the view
 * {@link #incomplete()}, and the confirmation step then discards the candidate.
 */
public final class BukkitWorldView implements WorldView {

    private static final double FOOTPRINT_HALF = 0.3;
    private static final double BELOW = 0.5000001;
    /** Boxes covering more blocks than this are treated as exempt rather than scanned. */
    private static final int MAX_SCANNED_BLOCKS = 256;
    /** Boxes spanning more chunks than this per axis are treated as unknown. */
    private static final int MAX_CHUNK_SPAN = 4;
    /** Highest vanilla block slipperiness (blue ice). */
    static final double MAX_SLIPPERINESS = 0.989;
    private static final double DEFAULT_SLIPPERINESS = 0.6;
    private static final UseEffects USE_EFFECTS = UseEffects.detect();

    private final World world;
    private final Player player;
    private final ChunkAccess chunks;
    private long lastReadableChunk;
    private boolean anyReadable;
    private boolean incomplete;

    public BukkitWorldView(World world, Player player, ChunkAccess chunks) {
        this.world = world;
        this.player = player;
        this.chunks = chunks;
    }

    /** True when a query needed a chunk that is not loaded or not owned by this region. */
    public boolean incomplete() {
        return incomplete;
    }

    @Override
    public double maxFriction(double x, double y, double z) {
        Material[] footprint = footprint(x, y, z);
        if (footprint == null) {
            return MAX_SLIPPERINESS;
        }
        double max = 0;
        for (Material type : footprint) {
            max = Math.max(max, friction(type));
        }
        return max;
    }

    @Override
    public double minFriction(double x, double y, double z) {
        Material[] footprint = footprint(x, y, z);
        if (footprint == null) {
            return DEFAULT_SLIPPERINESS;
        }
        double min = Double.POSITIVE_INFINITY;
        for (Material type : footprint) {
            min = Math.min(min, friction(type));
        }
        return min;
    }

    @Override
    public boolean soulSpeedBlockUnder(double x, double y, double z) {
        Material[] footprint = footprint(x, y, z);
        if (footprint == null) {
            return true;
        }
        for (Material type : footprint) {
            if (BlockKinds.soulSpeed(type)) {
                return true;
            }
        }
        return false;
    }

    @Override
    public boolean hasCollisions(Box box) {
        if (!readable(box)) {
            return true;
        }
        return world.hasCollisionsIn(bukkit(box));
    }

    @Override
    public boolean exemptNear(Box box) {
        if (!readable(box)) {
            return true;
        }
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
        Box expanded = box.expand(radius);
        if (!readable(expanded)) {
            return true;
        }
        BoundingBox search = bukkit(expanded);
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

    /** The four blocks under the footprint, or null (and incomplete) when one of their chunks is unreadable. */
    private Material[] footprint(double x, double y, double z) {
        int by = floor(y - BELOW);
        int minX = floor(x - FOOTPRINT_HALF);
        int maxX = floor(x + FOOTPRINT_HALF);
        int minZ = floor(z - FOOTPRINT_HALF);
        int maxZ = floor(z + FOOTPRINT_HALF);
        if (!readableBlocks(minX, minZ, maxX, maxZ)) {
            return null;
        }
        return new Material[]{
                world.getBlockAt(minX, by, minZ).getType(),
                world.getBlockAt(maxX, by, minZ).getType(),
                world.getBlockAt(minX, by, maxZ).getType(),
                world.getBlockAt(maxX, by, maxZ).getType()};
    }

    private boolean readable(Box box) {
        if (!Double.isFinite(box.minX()) || !Double.isFinite(box.maxX()) || !Double.isFinite(box.minZ())
                || !Double.isFinite(box.maxZ())) {
            incomplete = true;
            return false;
        }
        return readableBlocks(floor(box.minX()), floor(box.minZ()), floor(box.maxX()), floor(box.maxZ()));
    }

    /** Every chunk overlapped by the block range is readable; otherwise marks the view incomplete. */
    private boolean readableBlocks(int minX, int minZ, int maxX, int maxZ) {
        int fromX = minX >> 4;
        int toX = maxX >> 4;
        int fromZ = minZ >> 4;
        int toZ = maxZ >> 4;
        if (toX - fromX >= MAX_CHUNK_SPAN || toZ - fromZ >= MAX_CHUNK_SPAN) {
            incomplete = true;
            return false;
        }
        for (int cx = fromX; cx <= toX; cx++) {
            for (int cz = fromZ; cz <= toZ; cz++) {
                long key = ((long) cx << 32) | (cz & 0xffffffffL);
                if (anyReadable && key == lastReadableChunk) {
                    continue;
                }
                if (!chunks.readable(cx, cz)) {
                    incomplete = true;
                    return false;
                }
                lastReadableChunk = key;
                anyReadable = true;
            }
        }
        return true;
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
