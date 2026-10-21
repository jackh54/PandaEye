package dev.pandascript.pandaeye.e2e.probe;

import com.google.gson.JsonObject;
import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.bukkit.Bukkit;
import org.bukkit.entity.Player;

import java.lang.management.ManagementFactory;
import java.lang.management.ThreadInfo;
import java.lang.management.ThreadMXBean;
import java.util.Arrays;
import java.util.Map;
import java.util.TreeMap;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Tick timing that works on Paper and Folia: a 1-tick repeating task on the global region scheduler (global tick)
 * and one on every player's entity scheduler (the tick of the region that owns the player). TPS = 1000 / mean
 * interval. Paper's own getTPS/getAverageTickTime are added when the server supports them.
 */
final class TickMonitor {
    private static final int RING = 1200; // 60 s at 20 TPS

    private final ProbePlugin probe;
    private final Ring global = new Ring();
    private final Ring region = new Ring();
    private final ConcurrentHashMap<UUID, ScheduledTask> playerTasks = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<UUID, long[]> lastRun = new ConcurrentHashMap<>();
    private ScheduledTask globalTask;
    private long lastGlobal;

    TickMonitor(ProbePlugin probe) {
        this.probe = probe;
    }

    static final class Ring {
        private final long[] v = new long[RING];
        private int n, pos;

        synchronized void add(long ns) {
            v[pos] = ns;
            pos = (pos + 1) % RING;
            if (n < RING) n++;
        }

        synchronized void clear() {
            n = 0;
            pos = 0;
        }

        synchronized JsonObject json() {
            JsonObject o = new JsonObject();
            o.addProperty("samples", n);
            if (n == 0) return o;
            long[] c = Arrays.copyOf(n == RING ? v : Arrays.copyOfRange(v, 0, n), n);
            Arrays.sort(c);
            double sum = 0;
            for (long x : c) sum += x;
            double meanMs = sum / n / 1e6;
            o.addProperty("mean_interval_ms", meanMs);
            o.addProperty("tps", Math.min(20.0, 1000.0 / meanMs));
            o.addProperty("p50_interval_ms", c[n / 2] / 1e6);
            o.addProperty("p95_interval_ms", c[Math.min(n - 1, (int) (n * 0.95))] / 1e6);
            o.addProperty("p99_interval_ms", c[Math.min(n - 1, (int) (n * 0.99))] / 1e6);
            o.addProperty("max_interval_ms", c[n - 1] / 1e6);
            long late = 0;
            for (long x : c) if (x > 100_000_000L) late++;
            o.addProperty("intervals_over_100ms", late);
            return o;
        }
    }

    void start() {
        globalTask = Bukkit.getGlobalRegionScheduler().runAtFixedRate(probe, t -> {
            long now = System.nanoTime();
            if (lastGlobal != 0) global.add(now - lastGlobal);
            lastGlobal = now;
        }, 1, 1);
    }

    void stop() {
        if (globalTask != null) globalTask.cancel();
        for (ScheduledTask t : playerTasks.values()) t.cancel();
        playerTasks.clear();
    }

    void track(Player p) {
        UUID id = p.getUniqueId();
        long[] last = new long[1];
        lastRun.put(id, last);
        ScheduledTask t = p.getScheduler().runAtFixedRate(probe, task -> {
            long now = System.nanoTime();
            if (last[0] != 0) region.add(now - last[0]);
            last[0] = now;
        }, () -> playerTasks.remove(id), 1, 1);
        if (t != null) {
            ScheduledTask old = playerTasks.put(id, t);
            if (old != null) old.cancel();
        }
    }

    void untrack(Player p) {
        ScheduledTask t = playerTasks.remove(p.getUniqueId());
        if (t != null) t.cancel();
        lastRun.remove(p.getUniqueId());
    }

    void reset() {
        global.clear();
        region.clear();
    }

    JsonObject json() {
        JsonObject o = new JsonObject();
        o.add("global_region", global.json());
        o.add("player_regions", region.json());
        try {
            double[] tps = Bukkit.getTPS();
            o.addProperty("paper_tps_1m", tps[0]);
            o.addProperty("paper_tps_5m", tps[1]);
        } catch (Throwable ignored) {
            o.addProperty("paper_tps_1m", "unsupported");
        }
        try {
            o.addProperty("paper_mspt_avg", Bukkit.getAverageTickTime());
        } catch (Throwable ignored) {
            o.addProperty("paper_mspt_avg", "unsupported");
        }
        return o;
    }

    /** Process and per-thread-group CPU time (nanoseconds since JVM start). */
    static JsonObject cpuJson() {
        JsonObject o = new JsonObject();
        try {
            java.lang.management.OperatingSystemMXBean os = ManagementFactory.getOperatingSystemMXBean();
            if (os instanceof com.sun.management.OperatingSystemMXBean sun) {
                o.addProperty("process_cpu_ns", sun.getProcessCpuTime());
                o.addProperty("process_cpu_load", sun.getProcessCpuLoad());
            }
            o.addProperty("uptime_ms", ManagementFactory.getRuntimeMXBean().getUptime());
            ThreadMXBean tm = ManagementFactory.getThreadMXBean();
            Map<String, Long> groups = new TreeMap<>();
            Map<String, Long> counts = new TreeMap<>();
            if (tm.isThreadCpuTimeSupported()) {
                long[] ids = tm.getAllThreadIds();
                ThreadInfo[] infos = tm.getThreadInfo(ids);
                for (int i = 0; i < ids.length; i++) {
                    if (infos[i] == null) continue;
                    long cpu = tm.getThreadCpuTime(ids[i]);
                    if (cpu < 0) continue;
                    String g = group(infos[i].getThreadName());
                    groups.merge(g, cpu, Long::sum);
                    counts.merge(g, 1L, Long::sum);
                }
            }
            JsonObject go = new JsonObject();
            groups.forEach(go::addProperty);
            o.add("thread_group_cpu_ns", go);
            JsonObject co = new JsonObject();
            counts.forEach(co::addProperty);
            o.add("thread_group_count", co);
        } catch (Throwable t) {
            o.addProperty("error", String.valueOf(t));
        }
        return o;
    }

    static String group(String name) {
        if (name.startsWith("PandaEye")) return "pandaeye";
        String n = name.toLowerCase();
        if (n.contains("netty") || n.contains("epoll") || n.contains("io-")) return "netty";
        if (n.contains("region scheduler") || n.contains("tick thread") || n.equals("server thread") || n.contains("region")) return "tick";
        if (n.contains("worker") || n.contains("chunk") || n.contains("moonrise")) return "worker";
        if (n.contains("gc") || n.contains("g1")) return "gc";
        return "other";
    }
}
