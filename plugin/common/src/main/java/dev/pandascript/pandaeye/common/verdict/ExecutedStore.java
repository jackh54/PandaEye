package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.AtomicFiles;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

/**
 * {@code executed.json} (ARCHITECTURE 4.6/4.7): the verdict ids this server executed in the last 7 days (so a
 * verdict re-delivered after a restart is never executed twice), every ban this server executed (so an UNBAN
 * runs only for its own bans), the newest UNBAN time per player (a BAN older than it is never executed) and
 * the latched circuit-breaker state. Every change is written atomically (temp file + move) before the action
 * it records is performed; a change that cannot be written is rolled back and the action is not performed.
 * All collections are bounded; the oldest entries go first. Thread-safe.
 */
public final class ExecutedStore {

    public static final String FILE_NAME = "executed.json";
    public static final long KEEP_MILLIS = 7L * 24 * 3_600_000L;
    public static final int MAX_EXECUTED = 20_000;
    public static final int MAX_BANS = 10_000;
    public static final int MAX_UNBANS = 10_000;
    private static final int VERSION = 1;
    private static final long MAX_FILE_BYTES = 16L << 20;

    /** One executed verdict. {@code action} is the wire action without the {@code ACTION_} prefix. */
    public record Executed(String verdictId, String action, UUID player, long atUnixMs) {
    }

    /** A ban this server executed; {@code name} is the name the ban command used. */
    public record Ban(String verdictId, UUID player, String name, long atUnixMs) {
    }

    public record BreakerState(boolean open, long sinceUnixMs, String reason) {
        public static final BreakerState CLOSED = new BreakerState(false, 0, "");
    }

    private final Path file;
    private final Log log;
    private final LinkedHashMap<String, Executed> executed = new LinkedHashMap<>();
    private final LinkedHashMap<String, Ban> bans = new LinkedHashMap<>();
    private final LinkedHashMap<UUID, Long> unbans = new LinkedHashMap<>();
    private BreakerState breaker = BreakerState.CLOSED;

    private ExecutedStore(Path file, Log log) {
        this.file = file;
        this.log = log;
    }

    /**
     * Loads the store, dropping executed entries older than 7 days. A missing file is an empty store; an
     * unreadable one is moved aside to {@code executed.json.corrupt} with a loud warning.
     */
    public static ExecutedStore load(Path file, Log log, long nowUnixMs) {
        ExecutedStore store = new ExecutedStore(file, log);
        if (!Files.exists(file)) {
            return store;
        }
        try {
            if (Files.size(file) > MAX_FILE_BYTES) {
                throw new IOException("larger than " + (MAX_FILE_BYTES >> 20) + " MiB");
            }
            store.read(MiniJson.parse(Files.readString(file, StandardCharsets.UTF_8)), nowUnixMs);
        } catch (IOException | RuntimeException e) {
            store.clearAll();
            Path aside = file.resolveSibling(file.getFileName() + ".corrupt");
            try {
                Files.move(file, aside, StandardCopyOption.REPLACE_EXISTING);
            } catch (IOException moveError) {
                log.warn("could not move " + file.getFileName() + " aside: " + moveError);
            }
            log.warn("!!! " + file.getFileName() + " could not be read (" + e.getMessage() + "); it was moved to "
                    + aside.getFileName() + ". Verdicts executed before this start may be executed again if the"
                    + " cloud re-delivers them, and UNBANs for earlier bans will not run here. !!!");
        }
        return store;
    }

    public Path file() {
        return file;
    }

    public synchronized boolean executed(String verdictId) {
        return executed.containsKey(verdictId);
    }

    public synchronized Executed get(String verdictId) {
        return executed.get(verdictId);
    }

    public synchronized Ban ban(String verdictId) {
        return verdictId == null || verdictId.isEmpty() ? null : bans.get(verdictId);
    }

    /** Creation time of the newest UNBAN seen for the player, 0 when none. */
    public synchronized long lastUnban(UUID player) {
        Long at = unbans.get(player);
        return at == null ? 0 : at;
    }

    public synchronized int executedCount() {
        return executed.size();
    }

    public synchronized int banCount() {
        return bans.size();
    }

    /** Execution times of executed verdicts of one action since {@code sinceUnixMs} (breaker restore). */
    public synchronized List<Long> executedSince(String action, long sinceUnixMs) {
        List<Long> times = new ArrayList<>();
        for (Executed e : executed.values()) {
            if (e.action().equals(action) && e.atUnixMs() > sinceUnixMs) {
                times.add(e.atUnixMs());
            }
        }
        return times;
    }

    public synchronized BreakerState breaker() {
        return breaker;
    }

    /**
     * Records an execution (and the ban, for a BAN) and writes the file. On a write failure nothing is kept
     * and the exception is thrown: the caller must not perform the action.
     */
    public synchronized void recordExecuted(Executed entry, Ban ban, long nowUnixMs) throws IOException {
        Executed previousEntry = executed.remove(entry.verdictId());
        executed.put(entry.verdictId(), entry);
        Ban previousBan = null;
        if (ban != null) {
            previousBan = bans.remove(ban.verdictId());
            bans.put(ban.verdictId(), ban);
        }
        try {
            prune(nowUnixMs);
            save();
        } catch (IOException | RuntimeException e) {
            executed.remove(entry.verdictId());
            if (previousEntry != null) {
                executed.put(previousEntry.verdictId(), previousEntry);
            }
            if (ban != null) {
                bans.remove(ban.verdictId());
                if (previousBan != null) {
                    bans.put(previousBan.verdictId(), previousBan);
                }
            }
            throw e instanceof IOException io ? io : new IOException(e);
        }
    }

    /** Takes back an execution that was recorded but then not performed (best effort write). */
    public synchronized void rollback(String verdictId) {
        boolean changed = executed.remove(verdictId) != null;
        changed |= bans.remove(verdictId) != null;
        if (changed) {
            saveQuietly();
        }
    }

    /** Forgets a ban after its UNBAN ran (best effort write). */
    public synchronized void removeBan(String verdictId) {
        if (bans.remove(verdictId) != null) {
            saveQuietly();
        }
    }

    /** Remembers the creation time of an UNBAN for the player when it is newer than the known one. */
    public synchronized void noteUnban(UUID player, long createdUnixMs, long nowUnixMs) {
        if (player == null || createdUnixMs <= 0 || createdUnixMs <= lastUnban(player)) {
            return;
        }
        unbans.remove(player);
        unbans.put(player, createdUnixMs);
        prune(nowUnixMs);
        saveQuietly();
    }

    public synchronized void breaker(BreakerState state) {
        breaker = state;
        saveQuietly();
    }

    private void prune(long nowUnixMs) {
        long cutoff = nowUnixMs - KEEP_MILLIS;
        executed.values().removeIf(e -> e.atUnixMs() < cutoff);
        unbans.values().removeIf(at -> at < cutoff);
        trim(executed, MAX_EXECUTED);
        trim(bans, MAX_BANS);
        trim(unbans, MAX_UNBANS);
    }

    private static void trim(LinkedHashMap<?, ?> map, int max) {
        Iterator<?> it = map.keySet().iterator();
        while (map.size() > max && it.hasNext()) {
            it.next();
            it.remove();
        }
    }

    private void saveQuietly() {
        try {
            save();
        } catch (IOException e) {
            log.warn("could not write " + file.getFileName() + ": " + e);
        }
    }

    private void save() throws IOException {
        StringBuilder out = new StringBuilder(256 + executed.size() * 120 + bans.size() * 120);
        out.append("{\"version\":").append(VERSION).append(",\n\"executed\":[");
        boolean first = true;
        for (Executed e : executed.values()) {
            out.append(first ? "\n" : ",\n").append("{\"id\":");
            MiniJson.quote(out, e.verdictId());
            out.append(",\"action\":");
            MiniJson.quote(out, e.action());
            out.append(",\"player\":");
            MiniJson.quote(out, e.player() == null ? "" : e.player().toString());
            out.append(",\"at\":").append(e.atUnixMs()).append('}');
            first = false;
        }
        out.append("],\n\"bans\":[");
        first = true;
        for (Ban b : bans.values()) {
            out.append(first ? "\n" : ",\n").append("{\"id\":");
            MiniJson.quote(out, b.verdictId());
            out.append(",\"player\":");
            MiniJson.quote(out, b.player().toString());
            out.append(",\"name\":");
            MiniJson.quote(out, b.name());
            out.append(",\"at\":").append(b.atUnixMs()).append('}');
            first = false;
        }
        out.append("],\n\"unbans\":[");
        first = true;
        for (Map.Entry<UUID, Long> u : unbans.entrySet()) {
            out.append(first ? "\n" : ",\n").append("{\"player\":");
            MiniJson.quote(out, u.getKey().toString());
            out.append(",\"created\":").append(u.getValue()).append('}');
            first = false;
        }
        out.append("],\n\"breaker\":{\"open\":").append(breaker.open()).append(",\"since\":")
                .append(breaker.sinceUnixMs()).append(",\"reason\":");
        MiniJson.quote(out, breaker.reason());
        out.append("}}\n");
        AtomicFiles.writeString(file, out.toString(), false);
    }

    private void read(Object root, long nowUnixMs) {
        if (!(root instanceof Map<?, ?> doc)) {
            throw new IllegalArgumentException("not a JSON object");
        }
        for (Object item : list(doc.get("executed"))) {
            Map<?, ?> e = object(item);
            String id = string(e.get("id"));
            if (!id.isEmpty()) {
                executed.put(id, new Executed(id, string(e.get("action")), uuidOrNull(string(e.get("player"))),
                        number(e.get("at"))));
            }
        }
        for (Object item : list(doc.get("bans"))) {
            Map<?, ?> b = object(item);
            String id = string(b.get("id"));
            UUID player = uuidOrNull(string(b.get("player")));
            if (!id.isEmpty() && player != null) {
                bans.put(id, new Ban(id, player, string(b.get("name")), number(b.get("at"))));
            }
        }
        for (Object item : list(doc.get("unbans"))) {
            Map<?, ?> u = object(item);
            UUID player = uuidOrNull(string(u.get("player")));
            if (player != null) {
                unbans.put(player, number(u.get("created")));
            }
        }
        if (doc.get("breaker") instanceof Map<?, ?> b) {
            breaker = Boolean.TRUE.equals(b.get("open"))
                    ? new BreakerState(true, number(b.get("since")), string(b.get("reason")))
                    : BreakerState.CLOSED;
        }
        prune(nowUnixMs);
    }

    private void clearAll() {
        executed.clear();
        bans.clear();
        unbans.clear();
        breaker = BreakerState.CLOSED;
    }

    private static List<?> list(Object value) {
        return value instanceof List<?> l ? l : List.of();
    }

    private static Map<?, ?> object(Object value) {
        if (value instanceof Map<?, ?> m) {
            return m;
        }
        throw new IllegalArgumentException("expected an object");
    }

    private static String string(Object value) {
        return value instanceof String s ? s : "";
    }

    private static long number(Object value) {
        return value instanceof Number n ? n.longValue() : 0;
    }

    private static UUID uuidOrNull(String value) {
        try {
            return value.isEmpty() ? null : UUID.fromString(value);
        } catch (IllegalArgumentException e) {
            return null;
        }
    }
}
