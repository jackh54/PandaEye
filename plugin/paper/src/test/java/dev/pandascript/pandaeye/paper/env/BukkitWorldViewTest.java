package dev.pandascript.pandaeye.paper.env;

import dev.pandascript.pandaeye.paper.check.Box;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.block.Block;
import org.junit.jupiter.api.Test;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The world view never reads a chunk that is not loaded and owned by the region (no synchronous loads). */
class BukkitWorldViewTest {

    private final List<String> reads = new ArrayList<>();

    private World world(Material type) {
        Block block = (Block) Proxy.newProxyInstance(getClass().getClassLoader(), new Class<?>[]{Block.class},
                (proxy, method, args) -> switch (method.getName()) {
                    case "getType" -> type;
                    case "hashCode" -> 1;
                    default -> throw new UnsupportedOperationException(method.getName());
                });
        return (World) Proxy.newProxyInstance(getClass().getClassLoader(), new Class<?>[]{World.class},
                (proxy, method, args) -> switch (method.getName()) {
                    case "getBlockAt" -> {
                        reads.add("block " + args[0] + "," + args[2]);
                        yield block;
                    }
                    case "hasCollisionsIn" -> {
                        reads.add("collisions");
                        yield false;
                    }
                    case "getNearbyEntities" -> {
                        reads.add("entities");
                        yield List.of();
                    }
                    case "hashCode" -> 1;
                    default -> throw new UnsupportedOperationException(method.getName());
                });
    }

    @Test
    void readsInsideReadableChunksWork() {
        BukkitWorldView view = new BukkitWorldView(world(Material.SOUL_SAND), null, (cx, cz) -> cx == 0 && cz == 0);
        assertTrue(view.soulSpeedBlockUnder(8, 64, 8));
        assertEquals(4, reads.size());
        assertFalse(view.hasCollisions(new Box(7, 64, 7, 9, 66, 9)));
        assertFalse(view.collidableEntityNear(new Box(7, 64, 7, 9, 66, 9), 1));
        assertFalse(view.incomplete());
    }

    @Test
    void aQueryReachingAnUnreadableChunkReadsNothingAndMarksTheViewIncomplete() {
        BukkitWorldView view = new BukkitWorldView(world(Material.STONE), null, (cx, cz) -> cx == 0 && cz == 0);
        assertTrue(view.soulSpeedBlockUnder(15.8, 64, 8), "footprint reaches x = 16 (chunk 1): unknown = exempt");
        assertEquals(BukkitWorldView.MAX_SLIPPERINESS, view.maxFriction(15.8, 64, 8));
        assertTrue(view.exemptNear(new Box(15, 64, 7, 16.2, 66, 9)));
        assertTrue(view.hasCollisions(new Box(-0.5, 64, 7, 1, 66, 9)));
        assertTrue(view.collidableEntityNear(new Box(14, 64, 7, 15.5, 66, 9), 1));
        assertEquals(List.of(), reads, "nothing outside the readable chunk was touched");
        assertTrue(view.incomplete());
    }

    @Test
    void hugeOrNonFiniteBoxesAreUnknown() {
        BukkitWorldView view = new BukkitWorldView(world(Material.STONE), null, (cx, cz) -> true);
        assertTrue(view.hasCollisions(new Box(0, 0, 0, 200, 1, 1)));
        assertTrue(view.exemptNear(new Box(Double.NaN, 0, 0, 1, 1, 1)));
        assertEquals(List.of(), reads);
        assertTrue(view.incomplete());
    }
}
