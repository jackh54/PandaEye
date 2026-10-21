package dev.pandascript.pandaeye.velocity;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.TokenBucket;

import java.util.concurrent.atomic.LongAdder;

/** Verdict alerts on the proxy console: at most 20 lines per second (excess counted and reported). */
final class ConsoleAlerts {

    private final Log log;
    private final TokenBucket budget = new TokenBucket(20, 20, System.nanoTime());
    private final LongAdder suppressed = new LongAdder();

    ConsoleAlerts(Log log) {
        this.log = log;
    }

    void alert(String text) {
        boolean admitted;
        synchronized (budget) {
            admitted = budget.tryAcquire(System.nanoTime(), 1);
        }
        if (!admitted) {
            suppressed.increment();
            return;
        }
        long dropped = suppressed.sumThenReset();
        log.info("[alert] " + text + (dropped > 0 ? " [+" + dropped + " suppressed]" : ""));
    }

    void urgent(String text) {
        log.warn("[alert] " + text);
    }
}
