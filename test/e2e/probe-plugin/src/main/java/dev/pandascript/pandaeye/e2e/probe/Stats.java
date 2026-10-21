package dev.pandascript.pandaeye.e2e.probe;

import com.google.gson.JsonArray;
import com.google.gson.JsonObject;

import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLongArray;
import java.util.concurrent.atomic.LongAdder;

/** Thread-safe counters grouped by category ("wire.out", "pe.in.cancel_changed", ...), keyed by packet type. */
final class Stats {
    private final ConcurrentHashMap<String, ConcurrentHashMap<String, LongAdder>> groups = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<String, Hist> hists = new ConcurrentHashMap<>();

    void inc(String group, String key) {
        add(group, key, 1);
    }

    void add(String group, String key, long v) {
        groups.computeIfAbsent(group, g -> new ConcurrentHashMap<>())
                .computeIfAbsent(key, k -> new LongAdder()).add(v);
    }

    Hist hist(String name) {
        return hists.computeIfAbsent(name, n -> new Hist());
    }

    void clear() {
        groups.clear();
        hists.clear();
    }

    JsonObject groupsJson() {
        JsonObject o = new JsonObject();
        for (String g : new TreeMap<>(groups).keySet()) {
            JsonObject go = new JsonObject();
            Map<String, LongAdder> m = groups.get(g);
            if (m == null) continue;
            for (Map.Entry<String, LongAdder> e : new TreeMap<>(m).entrySet()) {
                long v = e.getValue().sum();
                if (v != 0) go.addProperty(e.getKey(), v);
            }
            o.add(g, go);
        }
        return o;
    }

    JsonObject histsJson() {
        JsonObject o = new JsonObject();
        for (Map.Entry<String, Hist> e : new TreeMap<>(hists).entrySet()) o.add(e.getKey(), e.getValue().json());
        return o;
    }

    /** Log2 histogram of nanosecond durations (count/sum/max exact, percentiles from buckets). */
    static final class Hist {
        private final LongAdder count = new LongAdder();
        private final LongAdder sum = new LongAdder();
        private final AtomicLongArray buckets = new AtomicLongArray(64);
        private volatile long max;

        void record(long ns) {
            if (ns < 0) ns = 0;
            count.increment();
            sum.add(ns);
            buckets.incrementAndGet(64 - Long.numberOfLeadingZeros(ns)); // 0 -> bucket 0, [2^(i-1), 2^i) -> i
            if (ns > max) max = ns; // racy but monotone enough for a test report
        }

        private double percentile(double p) {
            long n = count.sum();
            if (n == 0) return 0;
            long target = (long) Math.ceil(p * n);
            long seen = 0;
            for (int i = 0; i < 64; i++) {
                seen += buckets.get(i);
                if (seen >= target) return i == 0 ? 0 : (double) (1L << (i - 1)) * 1.5; // bucket midpoint
            }
            return max;
        }

        JsonObject json() {
            JsonObject o = new JsonObject();
            long n = count.sum();
            o.addProperty("count", n);
            o.addProperty("sum_ns", sum.sum());
            o.addProperty("avg_ns", n == 0 ? 0 : (double) sum.sum() / n);
            o.addProperty("p50_ns", percentile(0.50));
            o.addProperty("p99_ns", percentile(0.99));
            o.addProperty("max_ns", max);
            return o;
        }
    }

    static JsonArray arr(Iterable<String> it) {
        JsonArray a = new JsonArray();
        for (String s : it) a.add(s);
        return a;
    }
}
