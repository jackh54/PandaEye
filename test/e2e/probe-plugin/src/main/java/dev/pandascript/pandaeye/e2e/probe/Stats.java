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

    /**
     * Log-linear histogram of nanosecond durations: 8 sub-buckets per power of two (~9% resolution);
     * count/sum/max are exact, percentiles are bucket midpoints.
     */
    static final class Hist {
        private static final int SUB = 8;
        private final LongAdder count = new LongAdder();
        private final LongAdder sum = new LongAdder();
        private final AtomicLongArray buckets = new AtomicLongArray(64 * SUB);
        private volatile long max;

        private static int index(long ns) {
            if (ns < SUB) return (int) ns;
            int e = 63 - Long.numberOfLeadingZeros(ns); // floor(log2), >= 3
            int m = (int) ((ns >>> (e - 3)) & (SUB - 1)); // next 3 bits after the leading one
            return e * SUB + m;
        }

        private static double mid(int i) {
            if (i < SUB) return i;
            int e = i / SUB, m = i % SUB;
            double lo = (double) (1L << e) * (1 + m / (double) SUB);
            return lo * (1 + 0.5 / SUB);
        }

        void record(long ns) {
            if (ns < 0) ns = 0;
            count.increment();
            sum.add(ns);
            buckets.incrementAndGet(index(ns));
            if (ns > max) max = ns; // racy but monotone enough for a test report
        }

        private double percentile(double p) {
            long n = count.sum();
            if (n == 0) return 0;
            long target = (long) Math.ceil(p * n);
            long seen = 0;
            for (int i = 0; i < buckets.length(); i++) {
                seen += buckets.get(i);
                if (seen >= target) return Math.min(mid(i), max);
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
