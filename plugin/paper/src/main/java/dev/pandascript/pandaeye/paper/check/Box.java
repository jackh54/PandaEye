package dev.pandascript.pandaeye.paper.check;

/** An axis-aligned box in world coordinates. */
public record Box(double minX, double minY, double minZ, double maxX, double maxY, double maxZ) {

    public static final double PLAYER_WIDTH = 0.6;
    public static final double PLAYER_HEIGHT = 1.8;

    /** The player hitbox (scaled) standing at the given feet position. */
    public static Box player(double x, double y, double z, double scale) {
        double half = PLAYER_WIDTH * scale / 2;
        return new Box(x - half, y, z - half, x + half, y + PLAYER_HEIGHT * scale, z + half);
    }

    public Box expand(double amount) {
        return new Box(minX - amount, minY - amount, minZ - amount, maxX + amount, maxY + amount, maxZ + amount);
    }

    public Box union(Box other) {
        return new Box(Math.min(minX, other.minX), Math.min(minY, other.minY), Math.min(minZ, other.minZ),
                Math.max(maxX, other.maxX), Math.max(maxY, other.maxY), Math.max(maxZ, other.maxZ));
    }

    public Box shift(double dx, double dy, double dz) {
        return new Box(minX + dx, minY + dy, minZ + dz, maxX + dx, maxY + dy, maxZ + dz);
    }
}
