package dev.pandascript.pandaeye.paper.runtime;

import dev.pandascript.pandaeye.paper.check.CheckStatsWindow.Snapshot;
import dev.pandascript.pandaeye.paper.log.Json;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PeaksDigestTest {

    private static final UUID ALICE = UUID.fromString("00000000-0000-0000-0000-00000000000a");
    private static final UUID BOB = UUID.fromString("00000000-0000-0000-0000-00000000000b");
    private static final UUID CAROL = UUID.fromString("00000000-0000-0000-0000-00000000000c");

    @Test
    void byDefaultOnlyRowsWithCandidatesDiscardsOrConfirmationsAfterOneSummaryPerCheck() {
        PeaksDigest digest = new PeaksDigest(false, 300);
        digest.add(ALICE, "alice", "movement.speed", new Snapshot(0.2, 100, 0, 0, 0));
        digest.add(BOB, "bob", "movement.speed", new Snapshot(0.9, 100, 2, 1, 1));
        digest.add(CAROL, "carol", "movement.speed", new Snapshot(0.4, 50, 0, 1, 0)); // last window's candidate
        digest.add(ALICE, "alice", "dig.speed", new Snapshot(0.1, 10, 0, 0, 0));
        List<String> lines = lines(digest);
        assertEquals(List.of(
                "{\"time\":1000,\"summary\":true,\"check\":\"dig.speed\",\"players\":1,\"players_with_candidates\":0,"
                        + "\"players_confirmed\":0,\"evaluated\":10,\"candidates\":0,\"discarded\":0,\"confirmed\":0,"
                        + "\"max_stat_p50\":0.100000,\"max_stat_p90\":0.100000,\"max_stat_p99\":0.100000,"
                        + "\"max_stat_max\":0.100000,\"window_s\":300}",
                "{\"time\":1000,\"summary\":true,\"check\":\"movement.speed\",\"players\":3,"
                        + "\"players_with_candidates\":1,\"players_confirmed\":1,\"evaluated\":250,\"candidates\":2,"
                        + "\"discarded\":2,\"confirmed\":1,\"max_stat_p50\":0.400000,\"max_stat_p90\":0.900000,"
                        + "\"max_stat_p99\":0.900000,\"max_stat_max\":0.900000,\"window_s\":300}",
                "{\"time\":1000,\"player\":\"" + BOB + "\",\"name\":\"bob\",\"check\":\"movement.speed\","
                        + "\"max_stat\":0.900000,\"evaluated\":100,\"candidates\":2,\"discarded\":1,\"confirmed\":1,"
                        + "\"window_s\":300}",
                "{\"time\":1000,\"player\":\"" + CAROL + "\",\"name\":\"carol\",\"check\":\"movement.speed\","
                        + "\"max_stat\":0.400000,\"evaluated\":50,\"candidates\":0,\"discarded\":1,\"confirmed\":0,"
                        + "\"window_s\":300}"), lines);
    }

    @Test
    void summariesCarryNoPlayer() {
        PeaksDigest digest = new PeaksDigest(true, 300);
        digest.add(ALICE, "alice", "movement.speed", new Snapshot(3, 100, 4, 0, 4));
        String summary = lines(digest).get(0);
        assertTrue(summary.contains("\"summary\":true"));
        assertFalse(summary.contains("player\""), summary);
        assertFalse(summary.contains("alice") || summary.contains(ALICE.toString()), summary);
    }

    @Test
    void logAllPeaksKeepsEveryRow() {
        PeaksDigest digest = new PeaksDigest(true, 300);
        digest.add(ALICE, "alice", "movement.speed", new Snapshot(0.2, 100, 0, 0, 0));
        digest.add(BOB, "bob", "movement.speed", new Snapshot(0.9, 100, 2, 1, 1));
        digest.add(ALICE, "alice", "dig.speed", new Snapshot(0.1, 10, 0, 0, 0));
        List<String> lines = lines(digest);
        assertEquals(5, lines.size(), lines.toString());
        assertTrue(lines.get(0).contains("\"check\":\"dig.speed\",\"players\":1,"));
        assertTrue(lines.get(1).contains("\"check\":\"movement.speed\",\"players\":2,"));
        assertTrue(lines.get(2).contains("\"name\":\"alice\",\"check\":\"movement.speed\""));
        assertTrue(lines.get(3).contains("\"name\":\"bob\",\"check\":\"movement.speed\""));
        assertTrue(lines.get(4).contains("\"name\":\"alice\",\"check\":\"dig.speed\""));
    }

    @Test
    void maxStatPercentilesAreNearestRankOverThePlayers() {
        PeaksDigest digest = new PeaksDigest(false, 300);
        for (int i = 100; i >= 1; i--) {
            digest.add(UUID.randomUUID(), "p" + i, "timer.balance", new Snapshot(i, 20, 0, 0, 0));
        }
        List<String> lines = lines(digest);
        assertEquals(1, lines.size(), "quiet players get no rows");
        String summary = lines.get(0);
        assertTrue(summary.contains("\"players\":100,"), summary);
        assertTrue(summary.contains("\"evaluated\":2000,"), summary);
        assertTrue(summary.contains("\"max_stat_p50\":50.0000,\"max_stat_p90\":90.0000,\"max_stat_p99\":99.0000,"
                + "\"max_stat_max\":100.000,"), summary);
    }

    @Test
    void anEmptyWindowWritesNothing() {
        assertEquals(List.of(), lines(new PeaksDigest(true, 300)));
    }

    private static List<String> lines(PeaksDigest digest) {
        return digest.lines(1_000L).stream().map(Json::toString).toList();
    }
}
