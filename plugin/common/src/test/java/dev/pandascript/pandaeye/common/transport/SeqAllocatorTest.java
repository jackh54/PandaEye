package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.TestLog;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SeqAllocatorTest {

    @TempDir
    Path dir;

    @Test
    void reservesBlocksAndResumesAboveThemAfterARestart() throws Exception {
        Path file = dir.resolve("state.properties");
        SeqAllocator first = new SeqAllocator(new StateStore(file, new TestLog()));
        assertEquals(1, first.next());
        assertEquals(2, first.next());
        assertTrue(Files.readString(file).contains("seq.block-end=100000"));

        StateStore crashed = new StateStore(file, new TestLog());
        assertFalse(crashed.wasCleanShutdown());
        SeqAllocator second = new SeqAllocator(crashed);
        assertEquals(100_001, second.next());
        crashed.markCleanShutdown();
        assertTrue(new StateStore(file, new TestLog()).wasCleanShutdown());
    }

    @Test
    void reservesTheNextBlockWhenOneIsUsedUp() throws Exception {
        Path file = dir.resolve("state.properties");
        SeqAllocator seqs = new SeqAllocator(new StateStore(file, new TestLog()));
        for (long i = 1; i <= SeqAllocator.BLOCK; i++) {
            assertEquals(i, seqs.next());
        }
        assertEquals(SeqAllocator.BLOCK + 1, seqs.next());
        assertTrue(Files.readString(file).contains("seq.block-end=200000"));
    }

    @Test
    void neverHandsOutASeqTheCloudAlreadyHolds() {
        SeqAllocator seqs = new SeqAllocator(new StateStore(dir.resolve("state.properties"), new TestLog()));
        seqs.ensureAbove(7_000_000);
        assertEquals(7_000_001, seqs.next());
        seqs.ensureAbove(10);
        assertEquals(7_000_002, seqs.next());
    }

    @Test
    void corruptStateStartsFromScratchWithAWarning() throws Exception {
        Path file = dir.resolve("state.properties");
        Files.writeString(file, "seq.block-end=banana\n");
        TestLog log = new TestLog();
        assertEquals(1, new SeqAllocator(new StateStore(file, log)).next());
    }
}
