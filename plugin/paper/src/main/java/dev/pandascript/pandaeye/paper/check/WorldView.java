package dev.pandascript.pandaeye.paper.check;

/**
 * Read-only world queries for the confirmation step, valid only on the player's entity (region) thread. The
 * Bukkit implementation lives in {@code BukkitWorldView}; tests use fakes.
 */
public interface WorldView {

    /**
     * The highest block slipperiness (friction) under the 0.6 x 0.6 footprint at feet position (x, y, z),
     * sampled at {@code y - 0.5000001}. Air counts as 0.6 (vanilla's default friction).
     */
    double maxFriction(double x, double y, double z);

    /** The lowest block slipperiness under the footprint at feet position (x, y, z). */
    double minFriction(double x, double y, double z);

    /** Soul sand or soul soil under the footprint (soul speed can exceed every bound). */
    boolean soulSpeedBlockUnder(double x, double y, double z);

    /** Any block collision shape intersects the box ({@code World.hasCollisionsIn}). */
    boolean hasCollisions(Box box);

    /**
     * CHECKS 1.1 {@code exemptNear(box)}: liquid, bubble column, climbable, cobweb, sweet berry bush, powder
     * snow, honey, slime, bed, piston parts, or any pushable/collidable entity intersects the box.
     */
    boolean exemptNear(Box box);

    /** Any pushable or collidable entity other than the player within {@code radius} of the box. */
    boolean collidableEntityNear(Box box, double radius);

    /**
     * The movement-input multiplier of the item the player is using right now (0.2 in vanilla; 1.21.11+ items
     * may override it with the {@code use_effects} component). NaN when the player is not using an item or the
     * multiplier cannot be determined.
     */
    default double usingItemSpeedMultiplier() {
        return Double.NaN;
    }

    /**
     * Whether the block at (x, y, z) is not air right now: after the server handled a FINISH, the block is
     * still there when the server (or a protection plugin) refused the break. False when the chunk is not
     * readable, so callers discard.
     */
    default boolean blockPresent(int x, int y, int z) {
        return false;
    }
}
