package dev.pandascript.pandaeye.paper.state;

/**
 * The server's view of one C PLAYER_DIGGING START (dig.speed, CHECKS 1.6c), taken on the player's region
 * thread while the server handles the START, i.e. while the block still exists. Immutable.
 *
 * @param breakSpeed           Paper {@code Block#getBreakSpeed(Player)}: progress per tick, 1 = instant
 * @param instaBreak           the server (or a plugin) breaks the block on START
 * @param onGround             server-side on-ground state used by {@code breakSpeed} (airborne divides by 5)
 * @param underWater           eyes in water ({@code breakSpeed} includes the submerged mining factor)
 * @param digBoost             highest haste / conduit power amplifier, -1 for none
 * @param fatigue              mining fatigue amplifier, -1 for none
 * @param nanos                {@link System#nanoTime()} when the probe was taken
 */
public record DigProbe(
        int x,
        int y,
        int z,
        float breakSpeed,
        boolean instaBreak,
        boolean onGround,
        boolean underWater,
        int digBoost,
        int fatigue,
        double miningEfficiency,
        double blockBreakSpeed,
        double submergedMiningSpeed,
        long nanos) {

    public boolean at(int bx, int by, int bz) {
        return x == bx && y == by && z == bz;
    }
}
