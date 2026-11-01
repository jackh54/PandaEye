package dev.pandascript.pandaeye.paper.runtime;

import dev.pandascript.pandaeye.paper.check.CheckStatsWindow;
import dev.pandascript.pandaeye.paper.log.Json;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.UUID;

/**
 * The peaks-log lines of one stats window (ARCHITECTURE 4.9): per check one summary line without player fields
 * (players, summed counts, nearest-rank percentiles of the players' max statistic), then a row per player and
 * check that had a candidate, a discarded candidate or a confirmed flag, or every row with
 * {@code alerts.log-all-peaks}. Summaries come first so a peaks budget overrun costs rows, not summaries.
 */
final class PeaksDigest {

    private record Row(UUID player, String name, String check, CheckStatsWindow.Snapshot stats) {
    }

    private static final class Summary {
        double[] maxStats = new double[16];
        int players;
        int playersWithCandidates;
        int playersConfirmed;
        long evaluated;
        long candidates;
        long discarded;
        long confirmed;

        void add(CheckStatsWindow.Snapshot stats) {
            if (players == maxStats.length) {
                maxStats = Arrays.copyOf(maxStats, players * 2);
            }
            maxStats[players++] = stats.maxStat();
            if (stats.candidates() > 0) {
                playersWithCandidates++;
            }
            if (stats.confirmed() > 0) {
                playersConfirmed++;
            }
            evaluated += stats.evaluated();
            candidates += stats.candidates();
            discarded += stats.discarded();
            confirmed += stats.confirmed();
        }
    }

    private final boolean allRows;
    private final int windowSeconds;
    private final Map<String, Summary> summaries = new TreeMap<>();
    private final List<Row> rows = new ArrayList<>();

    PeaksDigest(boolean allRows, int windowSeconds) {
        this.allRows = allRows;
        this.windowSeconds = windowSeconds;
    }

    void add(UUID player, String name, String check, CheckStatsWindow.Snapshot stats) {
        summaries.computeIfAbsent(check, id -> new Summary()).add(stats);
        if (allRows || stats.candidates() > 0 || stats.discarded() > 0 || stats.confirmed() > 0) {
            rows.add(new Row(player, name, check, stats));
        }
    }

    /** The lines in write order (summaries by check id, then rows as added), all stamped {@code unixMillis}. */
    List<Json> lines(long unixMillis) {
        List<Json> lines = new ArrayList<>(summaries.size() + rows.size());
        summaries.forEach((check, summary) -> {
            double[] sorted = Arrays.copyOf(summary.maxStats, summary.players);
            Arrays.sort(sorted);
            lines.add(new Json()
                    .put("time", unixMillis)
                    .put("summary", true)
                    .put("check", check)
                    .put("players", summary.players)
                    .put("players_with_candidates", summary.playersWithCandidates)
                    .put("players_confirmed", summary.playersConfirmed)
                    .put("evaluated", summary.evaluated)
                    .put("candidates", summary.candidates)
                    .put("discarded", summary.discarded)
                    .put("confirmed", summary.confirmed)
                    .put("max_stat_p50", percentile(sorted, 50))
                    .put("max_stat_p90", percentile(sorted, 90))
                    .put("max_stat_p99", percentile(sorted, 99))
                    .put("max_stat_max", sorted[sorted.length - 1])
                    .put("window_s", windowSeconds));
        });
        for (Row row : rows) {
            CheckStatsWindow.Snapshot stats = row.stats();
            lines.add(new Json()
                    .put("time", unixMillis)
                    .put("player", row.player().toString())
                    .put("name", row.name())
                    .put("check", row.check())
                    .put("max_stat", stats.maxStat())
                    .put("evaluated", stats.evaluated())
                    .put("candidates", stats.candidates())
                    .put("discarded", stats.discarded())
                    .put("confirmed", stats.confirmed())
                    .put("window_s", windowSeconds));
        }
        return lines;
    }

    /** Nearest-rank percentile of a sorted, non-empty array. */
    private static double percentile(double[] sorted, double p) {
        int rank = (int) Math.ceil(p / 100.0 * sorted.length);
        return sorted[Math.min(sorted.length, Math.max(1, rank)) - 1];
    }
}
