package dev.pandascript.pandaeye.paper.check;

/**
 * Tuning statistics of one check for one player over the current window (ARCHITECTURE 4.5): max test
 * statistic, evaluated, candidates, candidates discarded by the confirmation, confirmed. Thread-safe: the
 * netty thread evaluates, the entity thread confirms, an async task drains every 5 minutes.
 */
public final class CheckStatsWindow {

    public record Snapshot(double maxStat, int evaluated, int candidates, int discarded, int confirmed) {

        public boolean empty() {
            return evaluated == 0 && candidates == 0 && confirmed == 0;
        }
    }

    private double maxStat = Double.NEGATIVE_INFINITY;
    private int evaluated;
    private int candidates;
    private int discarded;
    private int confirmed;

    public synchronized void evaluated(double statistic) {
        evaluated++;
        if (!Double.isNaN(statistic) && statistic > maxStat) {
            maxStat = statistic;
        }
    }

    public synchronized void candidate() {
        candidates++;
    }

    public synchronized void discarded() {
        discarded++;
    }

    public synchronized void confirmed() {
        confirmed++;
    }

    public synchronized Snapshot drain() {
        Snapshot snapshot = new Snapshot(maxStat == Double.NEGATIVE_INFINITY ? 0 : maxStat, evaluated, candidates,
                discarded, confirmed);
        maxStat = Double.NEGATIVE_INFINITY;
        evaluated = 0;
        candidates = 0;
        discarded = 0;
        confirmed = 0;
        return snapshot;
    }
}
