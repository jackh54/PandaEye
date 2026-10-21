package dev.pandascript.pandaeye.paper.flag;

import dev.pandascript.pandaeye.paper.check.ConfirmContext.Position;
import dev.pandascript.pandaeye.paper.env.ChunkAccess;
import org.junit.jupiter.api.Test;

import java.util.HashSet;
import java.util.List;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ConfirmationGuardTest {

    private static final class Chunks implements ChunkAccess {
        final Set<Long> readable = new HashSet<>();
        final Set<Long> asked = new HashSet<>();

        Chunks allow(int cx, int cz) {
            readable.add(key(cx, cz));
            return this;
        }

        @Override
        public boolean readable(int chunkX, int chunkZ) {
            asked.add(key(chunkX, chunkZ));
            return readable.contains(key(chunkX, chunkZ));
        }

        static long key(int cx, int cz) {
            return ((long) cx << 32) | (cz & 0xffffffffL);
        }
    }

    @Test
    void positionsInsideOneReadableChunkPass() {
        Chunks chunks = new Chunks().allow(0, 0);
        assertNull(ConfirmationGuard.check(List.of(new Position(5, 64, 5), new Position(6, 64, 6)), 6, 64, 6, chunks));
        assertEquals(Set.of(Chunks.key(0, 0)), chunks.asked);
    }

    @Test
    void aNeighbourChunkWithinOneBlockMustBeReadableToo() {
        // feet at x = 15.5: the footprint (+0.3) and exempt boxes (+0.6) reach into chunk 1
        Chunks onlyOwn = new Chunks().allow(0, 0);
        List<Position> positions = List.of(new Position(15.2, 64, 8), new Position(15.5, 64, 8));
        assertNotNull(ConfirmationGuard.check(positions, 15.5, 64, 8, onlyOwn));
        assertTrue(onlyOwn.asked.contains(Chunks.key(1, 0)));

        Chunks both = new Chunks().allow(0, 0).allow(1, 0);
        assertNull(ConfirmationGuard.check(positions, 15.5, 64, 8, both));

        Chunks negative = new Chunks().allow(-1, -1).allow(0, -1).allow(-1, 0);
        assertNotNull(ConfirmationGuard.check(List.of(new Position(-0.5, 64, -0.5)), -0.5, 64, -0.5, negative),
                "-0.5 + 1 reaches chunk 0");
        negative.allow(0, 0);
        assertNull(ConfirmationGuard.check(List.of(new Position(-0.5, 64, -0.5)), -0.5, 64, -0.5, negative));
    }

    @Test
    void positionsFarFromThePlayerOrNotFiniteAreDiscarded() {
        ChunkAccess all = (cx, cz) -> true;
        assertNull(ConfirmationGuard.check(List.of(new Position(7.9, 64, 0)), 0, 64, 0, all));
        assertNotNull(ConfirmationGuard.check(List.of(new Position(8.1, 64, 0)), 0, 64, 0, all));
        assertNotNull(ConfirmationGuard.check(List.of(new Position(0, 74, 0)), 0, 64, 0, all));
        assertNotNull(ConfirmationGuard.check(List.of(new Position(Double.NaN, 64, 0)), 0, 64, 0, all));
        assertNotNull(ConfirmationGuard.check(List.of(new Position(0, 64, Double.POSITIVE_INFINITY)), 0, 64, 0, all));
    }

    @Test
    void noPositionsStillRequiresThePlayersOwnSurroundings() {
        Chunks none = new Chunks();
        assertNotNull(ConfirmationGuard.check(List.of(), 3, 64, 3, none));
    }
}
