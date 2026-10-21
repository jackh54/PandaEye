package dev.pandascript.pandaeye.paper.env;

import org.bukkit.Bukkit;
import org.bukkit.World;

/**
 * Whether a chunk may be read from the current thread without side effects: loaded, and owned by the current
 * Folia region. Reading anything else would load or generate the chunk synchronously, or touch another
 * region's data.
 */
@FunctionalInterface
public interface ChunkAccess {

    boolean readable(int chunkX, int chunkZ);

    /** Loaded and owned by the region of the calling thread. */
    static ChunkAccess currentRegion(World world) {
        return (chunkX, chunkZ) -> world.isChunkLoaded(chunkX, chunkZ)
                && Bukkit.isOwnedByCurrentRegion(world, chunkX, chunkZ);
    }
}
