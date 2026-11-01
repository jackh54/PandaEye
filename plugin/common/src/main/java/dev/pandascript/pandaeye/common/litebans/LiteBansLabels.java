package dev.pandascript.pandaeye.common.litebans;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.ModerationLabel;

import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.util.Locale;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.LongAdder;
import java.util.function.Consumer;
import java.util.function.LongSupplier;

/**
 * LiteBans as a source of confirm/overturn labels (ARCHITECTURE 4.12): bans that {@link BanClassifier} labels CHEAT
 * or LEGIT become {@link ModerationLabel} events in the normal event sink, so they are batched, buffered and spooled
 * like every other event (offline players and the proxy included). Optional: without LiteBans, with
 * {@code labels.litebans.enabled: false} or with the cloud off nothing is registered, scheduled or sent and no
 * LiteBans class is loaded; {@link LiteBansHook} is the only class that touches LiteBans' API.
 *
 * <p>Threads: LiteBans calls back on its own threads; a callback is cheap (reason matching, one event into the
 * lock-free sink) and catches everything. The backfill (the bans of the last {@code backfill-days}) runs once per
 * enable on the platform's async scheduler, never on the main or a region thread.
 */
public final class LiteBansLabels {

    public static final String SOURCE = "litebans";
    static final String API_CLASS = "litebans.api.Events";
    /** LiteBans replaces {@code {bans}} with its bans table. */
    static final String HISTORY_SQL = "SELECT id, uuid, reason, banned_by_uuid, removed_by_reason, removed_by_date,"
            + " time, until FROM {bans} WHERE time >= ? ORDER BY id";
    static final int MAX_BACKFILL_ROWS = 50_000;
    /** LiteBans' API was not up at enable: one more attempt this much later. */
    static final long RETRY_DELAY_MS = 10_000;

    /** What PandaEye needs from LiteBans: {@link LiteBansHook}, or a fake in tests. */
    interface Api {

        /** Registers the callbacks of {@code labels}; false while LiteBans' API is not up yet. */
        boolean register(LiteBansLabels labels);

        void unregister();

        /** A statement on LiteBans' own database connection. */
        PreparedStatement prepare(String sql) throws SQLException;
    }

    /** Runs a task once on the platform's async scheduler (never a server thread) after {@code delayMillis}. */
    @FunctionalInterface
    public interface Async {
        void run(Runnable task, long delayMillis);
    }

    private enum Registration {
        ON,
        NOT_READY,
        FAILED
    }

    private final LiteBansSettings settings;
    private final BanClassifier classifier;
    private final Api api;
    private final Consumer<Event> events;
    private final EventClock clock;
    private final LongSupplier unixMillis;
    private final Async async;
    private final Log log;
    private final RateLimitedLog errors;
    private final LongAdder cheat = new LongAdder();
    private final LongAdder legit = new LongAdder();
    private volatile String state;
    private volatile String backfill = "pending";
    private volatile boolean stopped;
    private boolean registered;

    LiteBansLabels(LiteBansSettings settings, Api api, Consumer<Event> events, EventClock clock, LongSupplier unixMillis,
                   Async async, Log log, String state) {
        this.settings = settings;
        this.classifier = new BanClassifier(settings);
        this.api = api;
        this.events = events;
        this.clock = clock;
        this.unixMillis = unixMillis;
        this.async = async;
        this.log = log;
        this.errors = new RateLimitedLog(log, TimeUnit.SECONDS.toNanos(10));
        this.state = state;
    }

    /**
     * Labels for this plugin. {@code loader} is the plugin's class loader (it sees LiteBans' classes when LiteBans is
     * installed and declared as an optional dependency); {@code events} is the transport's sink, null when the cloud
     * is off.
     */
    public static LiteBansLabels create(LiteBansSettings settings, ClassLoader loader, Consumer<Event> events,
                                        EventClock clock, Async async, Log log) {
        String off = !settings.enabled() ? "off (labels.litebans.enabled: false)"
                : events == null ? "off (cloud off)"
                : !installed(loader) ? "off (LiteBans not installed)"
                : null;
        Api api = null;
        if (off == null) {
            try {
                api = new LiteBansHook();
            } catch (LinkageError e) {
                log.warn("LiteBans is installed but its API is not the expected one (" + e
                        + "); LiteBans labels are off");
                off = "off (LiteBans API not compatible)";
            }
        }
        return new LiteBansLabels(settings, api, events, clock, System::currentTimeMillis, async, log,
                off == null ? "starting" : off);
    }

    static boolean installed(ClassLoader loader) {
        try {
            Class.forName(API_CLASS, false, loader);
            return true;
        } catch (ClassNotFoundException | LinkageError e) {
            return false;
        }
    }

    /** Registers with LiteBans and schedules the backfill (or one retry 10 s later if LiteBans is not up yet). */
    public void start() {
        if (api == null) {
            return;
        }
        switch (register()) {
            case ON -> {
                if (settings.backfillDays() > 0) {
                    async.run(this::backfill, 0);
                } else {
                    backfill();
                }
            }
            case NOT_READY -> {
                state = "waiting for LiteBans";
                async.run(this::retry, RETRY_DELAY_MS);
            }
            case FAILED -> {
            }
        }
    }

    /** Unregisters and stops a running backfill at its next row. Safe to call twice. */
    public synchronized void stop() {
        stopped = true;
        if (registered) {
            registered = false;
            try {
                api.unregister();
            } catch (Throwable t) {
                log.warn("could not unregister from LiteBans: " + t);
            }
        }
    }

    /** One line for the status commands. */
    public String status() {
        String current = state;
        if (!current.equals("on")) {
            return current;
        }
        return String.format(Locale.ROOT, "on, %d cheat and %d legit labels sent; backfill %s", cheat.sum(),
                legit.sum(), backfill);
    }

    // ---------------------------------------------------------------------------------------------
    // LiteBans callbacks (any thread)
    // ---------------------------------------------------------------------------------------------

    void added(BanEntry ban) {
        if (!stopped) {
            label(ban, ModerationLabel.Kind.KIND_BAN_ADDED, classifier.added(ban), 0);
        }
    }

    void removed(BanEntry ban) {
        if (!stopped) {
            label(ban, ModerationLabel.Kind.KIND_BAN_REMOVED, classifier.removed(ban), unixMillis.getAsLong());
        }
    }

    /** A callback threw (an unexpected LiteBans entry or API): reported at most every 10 s. */
    void failed(Throwable error) {
        errors.warn("litebans", "could not label a LiteBans entry", error);
    }

    // ---------------------------------------------------------------------------------------------

    private synchronized Registration register() {
        if (stopped) {
            return Registration.FAILED;
        }
        try {
            if (!api.register(this)) {
                return Registration.NOT_READY;
            }
        } catch (Throwable t) {
            log.warn("could not register with LiteBans (" + t + "); LiteBans labels are off");
            state = "off (LiteBans API not compatible)";
            return Registration.FAILED;
        }
        registered = true;
        state = "on";
        log.info("LiteBans labels on: cheating bans and wrongful unbans are sent to the cloud (labels.litebans)");
        return Registration.ON;
    }

    private void retry() {
        switch (register()) {
            case ON -> backfill();
            case NOT_READY -> {
                state = "off (LiteBans API not ready)";
                log.warn("LiteBans' API was still not ready " + RETRY_DELAY_MS / 1_000
                        + " s after enable; LiteBans labels are off until the next restart");
            }
            case FAILED -> {
            }
        }
    }

    /**
     * Async thread only (it queries LiteBans' database): labels the bans of the last {@code backfill-days}; the cloud
     * de-duplicates what it already has. Stops at the next row once the plugin stops.
     */
    void backfill() {
        int days = settings.backfillDays();
        if (days == 0) {
            backfill = "off (backfill-days: 0)";
            return;
        }
        if (stopped) {
            return;
        }
        long cheatBefore = cheat.sum();
        long legitBefore = legit.sum();
        int rows = 0;
        try (PreparedStatement statement = api.prepare(HISTORY_SQL)) {
            statement.setMaxRows(MAX_BACKFILL_ROWS);
            statement.setLong(1, unixMillis.getAsLong() - TimeUnit.DAYS.toMillis(days));
            try (ResultSet result = statement.executeQuery()) {
                while (result.next()) {
                    if (stopped) {
                        break;
                    }
                    rows++;
                    BanEntry ban = new BanEntry(result.getLong("id"), "ban", result.getString("uuid"),
                            result.getString("reason"), result.getString("banned_by_uuid"),
                            result.getString("removed_by_reason"), result.getLong("time"), result.getLong("until"));
                    label(ban, ModerationLabel.Kind.KIND_BAN_ADDED, classifier.added(ban), 0);
                    if (ban.removalReason() != null && !ban.removalReason().isEmpty()) {
                        label(ban, ModerationLabel.Kind.KIND_BAN_REMOVED, classifier.removed(ban), removedAt(result));
                    }
                }
            }
        } catch (SQLException | RuntimeException | LinkageError e) {
            backfill = "failed after " + rows + " bans (" + e + ")";
            log.warn("LiteBans backfill failed after " + rows + " bans (" + e + "); live LiteBans labels are unaffected");
            return;
        }
        backfill = String.format(Locale.ROOT, "%d bans of the last %d days read%s", rows, days,
                stopped ? " (stopped)" : "");
        log.info(String.format(Locale.ROOT, "LiteBans backfill: %d bans of the last %d days read, %d cheat and %d legit"
                + " labels sent", rows, days, cheat.sum() - cheatBefore, legit.sum() - legitBefore));
    }

    /** The unban time of a history row; 0 when unknown (some drivers refuse MySQL's zero date). */
    private static long removedAt(ResultSet result) {
        try {
            Timestamp at = result.getTimestamp("removed_by_date");
            return at == null ? 0 : at.getTime();
        } catch (SQLException e) {
            return 0;
        }
    }

    private void label(BanEntry ban, ModerationLabel.Kind kind, BanClassifier.Label label, long removedMs) {
        if (label == BanClassifier.Label.NONE || ban.id() <= 0) {
            return;
        }
        UUID player = player(ban.player());
        if (player == null) {
            return;
        }
        events.accept(event(ban, player, kind, label, removedMs, clock.nowMicros()));
        (label == BanClassifier.Label.CHEAT ? cheat : legit).increment();
        log.debug("LiteBans label: ban #" + ban.id() + " " + kind.name().substring(5).toLowerCase(Locale.ROOT)
                + " " + label.name().toLowerCase(Locale.ROOT));
    }

    /** The event for one label: no reason text, no executor or remover identity, no IP (PROTOCOL 5). */
    static Event event(BanEntry ban, UUID player, ModerationLabel.Kind kind, BanClassifier.Label label, long removedMs,
                       long timeUs) {
        return Event.newBuilder()
                .setPlayer(Uuids.toByteString(player))
                .setTimeUs(timeUs)
                .setLabel(ModerationLabel.newBuilder()
                        .setSource(SOURCE)
                        .setEntryId(ban.id())
                        .setKind(kind)
                        .setClassification(label == BanClassifier.Label.CHEAT
                                ? ModerationLabel.Classification.CLASSIFICATION_CHEAT
                                : ModerationLabel.Classification.CLASSIFICATION_LEGIT)
                        .setBanStartUnixMs(Math.max(0, ban.startMs()))
                        .setRemovedUnixMs(Math.max(0, removedMs))
                        .setPermanent(ban.permanent())
                        .setExecutor(BanClassifier.console(ban.executor())
                                ? ModerationLabel.Executor.EXECUTOR_CONSOLE
                                : ModerationLabel.Executor.EXECUTOR_STAFF))
                .build();
    }

    /** The banned player's UUID as LiteBans gives it (Bedrock players included); null when there is none. */
    private static UUID player(String uuid) {
        if (uuid == null || uuid.isBlank()) {
            return null;
        }
        try {
            return UUID.fromString(uuid.trim());
        } catch (IllegalArgumentException e) {
            return null;
        }
    }
}
