package dev.pandascript.pandaeye.paper.flag;

import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.env.ChunkAccess;

import java.util.List;

/**
 * The position guards of the confirmation step, free of Bukkit so they can be tested: every position must be
 * within 8 blocks of the player, and every chunk overlapped by the box around the positions and the player,
 * expanded by one block, must be readable (loaded and owned by the current region). Confirmers read blocks
 * up to about 0.6 blocks around the positions, so without this a candidate at a chunk border could force a
 * synchronous chunk load on the region thread.
 */
final class ConfirmationGuard {

    static final double MAX_DISTANCE = 8;
    static final double MARGIN = 1;

    private ConfirmationGuard() {
    }

    /** Null when the candidate may be confirmed, else the discard reason. */
    static String check(List<ConfirmContext.Position> positions, double playerX, double playerY, double playerZ,
                        ChunkAccess chunks) {
        double minX = playerX;
        double maxX = playerX;
        double minZ = playerZ;
        double maxZ = playerZ;
        for (ConfirmContext.Position p : positions) {
            double dx = p.x() - playerX;
            double dy = p.y() - playerY;
            double dz = p.z() - playerZ;
            if (!(dx * dx + dy * dy + dz * dz <= MAX_DISTANCE * MAX_DISTANCE)) {
                return "position more than 8 blocks from the player";
            }
            minX = Math.min(minX, p.x());
            maxX = Math.max(maxX, p.x());
            minZ = Math.min(minZ, p.z());
            maxZ = Math.max(maxZ, p.z());
        }
        if (!Double.isFinite(minX) || !Double.isFinite(maxX) || !Double.isFinite(minZ) || !Double.isFinite(maxZ)) {
            return "position not finite";
        }
        int fromX = (int) Math.floor(minX - MARGIN) >> 4;
        int toX = (int) Math.floor(maxX + MARGIN) >> 4;
        int fromZ = (int) Math.floor(minZ - MARGIN) >> 4;
        int toZ = (int) Math.floor(maxZ + MARGIN) >> 4;
        for (int cx = fromX; cx <= toX; cx++) {
            for (int cz = fromZ; cz <= toZ; cz++) {
                if (!chunks.readable(cx, cz)) {
                    return "chunk not loaded or outside the player's region";
                }
            }
        }
        return null;
    }
}
