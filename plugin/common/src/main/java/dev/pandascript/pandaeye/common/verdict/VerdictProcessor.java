package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Action;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import dev.pandascript.pandaeye.protocol.v1.Verdict;

import java.io.IOException;
import java.util.EnumSet;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

/**
 * Verdict handling shared by the Paper and Velocity plugins (ARCHITECTURE 4.7, PROTOCOL 7.2-7.4). Every
 * verdict is de-duplicated by id (in memory, and across restarts through {@link ExecutedStore}), logged,
 * announced to staff and answered with exactly one {@code VerdictAck} per delivery. It is executed only when
 * every gate passes, checked in this order:
 *
 * <ol>
 *   <li>UNBAN is special: it runs in any mode when {@code supersedes} is a ban this server executed, else
 *       {@link VerdictNotes#NOT_EXECUTED_HERE};</li>
 *   <li>shadow: local mode, last-known {@code RemoteConfig.enforcement} false, or {@code verdict.shadow};</li>
 *   <li>staleness: KICK older than 5 min, BAN older than 24 h, BAN not newer than the player's last UNBAN;</li>
 *   <li>the player: resolved locally to a valid name (online, else the offline cache), online for KICK and
 *       MITIGATE, not exempt;</li>
 *   <li>the circuit breaker (KICK and BAN count; an open breaker also stops MITIGATE);</li>
 *   <li>the execution is recorded in {@code executed.json} before it is performed (at most once).</li>
 * </ol>
 *
 * UNMITIGATE clears active mitigations in any mode (it only removes PandaEye's own enforcement). Runs on the
 * cloud thread; thread-safe.
 */
public final class VerdictProcessor {

    public static final long KICK_MAX_AGE_MILLIS = 5 * 60_000L;
    public static final long BAN_MAX_AGE_MILLIS = 24 * 3_600_000L;
    static final int REMEMBERED = 10_000;
    private static final String DEFAULT_KICK_MESSAGE = "Unfair advantage detected";

    /** Sends a VerdictAck. */
    @FunctionalInterface
    public interface Acknowledger {
        void ack(String verdictId, boolean executed, String note);
    }

    public record Outcome(boolean executed, String note) {
    }

    /** One handled verdict for the local verdict log. {@code name} is the locally resolved name or "". */
    public record Logged(Verdict verdict, UUID player, String name, Outcome outcome, boolean duplicate,
                         long atUnixMs) {
    }

    private final VerdictPlatform platform;
    private final ExecutedStore store;
    private final Acknowledger acks;
    private final Log log;
    private final CircuitBreaker breaker;
    private final Map<String, Outcome> answered = new LinkedHashMap<>(256, 0.75f, false) {
        @Override
        protected boolean removeEldestEntry(Map.Entry<String, Outcome> eldest) {
            return size() > REMEMBERED;
        }
    };

    public VerdictProcessor(VerdictPlatform platform, ExecutedStore store, Acknowledger acks, Log log) {
        this.platform = platform;
        this.store = store;
        this.acks = acks;
        this.log = log;
        PunishmentSettings punishments = platform.punishments();
        this.breaker = new CircuitBreaker(punishments.maxBansPerHour(), punishments.maxKicksPer10Min());
        long now = platform.nowUnixMillis();
        ExecutedStore.BreakerState state = store.breaker();
        breaker.restore(store.executedSince("BAN", now - CircuitBreaker.BAN_WINDOW_MILLIS),
                store.executedSince("KICK", now - CircuitBreaker.KICK_WINDOW_MILLIS), state.open(),
                state.sinceUnixMs(), state.reason());
        if (state.open()) {
            log.warn("the punishment circuit breaker is still OPEN from an earlier run (" + state.reason()
                    + "): no kicks, bans or mitigations until /pandaeye breaker reset");
        }
    }

    /** Handles one delivery of a verdict and acknowledges it. Never throws. */
    public Outcome handle(Verdict verdict) {
        String id = verdict.getVerdictId();
        Outcome outcome;
        boolean fresh = false;
        boolean duplicate = false;
        long now = platform.nowUnixMillis();
        synchronized (this) {
            outcome = answered.get(id);
            if (outcome == null) {
                if (id.isEmpty() || id.length() > 128) {
                    outcome = new Outcome(false, VerdictNotes.INVALID);
                } else if (store.executed(id)) {
                    outcome = new Outcome(true, VerdictNotes.ALREADY_EXECUTED);
                    duplicate = true;
                } else {
                    fresh = true;
                    try {
                        outcome = decide(verdict, now);
                    } catch (Throwable t) {
                        log.warn("verdict " + Placeholders.token(id, 64) + " could not be handled", t);
                        outcome = new Outcome(false, "not executed: internal error");
                    }
                }
                if (!id.isEmpty()) {
                    answered.put(id, outcome);
                }
            } else {
                duplicate = true;
            }
        }
        UUID player = Uuids.fromByteString(verdict.getPlayer()).orElse(null);
        String name = player == null ? "" : displayName(player, verdict);
        if (fresh) {
            announce(verdict, name, outcome);
        }
        try {
            platform.logged(new Logged(verdict, player, name, outcome, duplicate, now));
        } catch (Throwable t) {
            log.warn("could not log verdict " + Placeholders.token(id, 64), t);
        }
        acks.ack(id, outcome.executed(), outcome.note());
        return outcome;
    }

    public CircuitBreaker.Status breakerStatus() {
        return breaker.status(platform.nowUnixMillis());
    }

    /** {@code /pandaeye breaker reset}. */
    public void resetBreaker(String by) {
        breaker.reset();
        store.breaker(ExecutedStore.BreakerState.CLOSED);
        log.warn("punishment circuit breaker reset by " + by);
    }

    public ExecutedStore store() {
        return store;
    }

    // ---------------------------------------------------------------------------------------------

    private Outcome decide(Verdict v, long now) {
        Action action = v.getAction();
        UUID player = Uuids.fromByteString(v.getPlayer()).orElse(null);
        if (action == Action.ACTION_UNBAN) {
            return unban(v, player, now);
        }
        if (action == Action.ACTION_UNMITIGATE && player != null && platform.unmitigate(player)) {
            recordQuietly(v, player, now);
            return new Outcome(true, VerdictNotes.UNMITIGATED);
        }
        switch (action) {
            case ACTION_ALERT, ACTION_MITIGATE, ACTION_UNMITIGATE, ACTION_KICK, ACTION_BAN -> {
            }
            default -> {
                return new Outcome(false, VerdictNotes.UNSUPPORTED_ACTION);
            }
        }
        String shadow = shadowReason(v);
        if (shadow != null) {
            return new Outcome(false, shadow);
        }
        if (action == Action.ACTION_ALERT) {
            return new Outcome(true, VerdictNotes.ALERTED);
        }
        if (player == null) {
            return new Outcome(false, VerdictNotes.UNKNOWN_PLAYER);
        }
        long created = v.getCreatedUnixMs();
        long age = created <= 0 ? Long.MAX_VALUE : Math.max(0, now - created);
        if (action == Action.ACTION_KICK && age > KICK_MAX_AGE_MILLIS) {
            return new Outcome(false, VerdictNotes.STALE_KICK);
        }
        if (action == Action.ACTION_BAN && age > BAN_MAX_AGE_MILLIS) {
            return new Outcome(false, VerdictNotes.STALE_BAN);
        }
        if (action == Action.ACTION_BAN && created <= store.lastUnban(player)) {
            return new Outcome(false, VerdictNotes.BAN_BEFORE_UNBAN);
        }
        return switch (action) {
            case ACTION_MITIGATE -> mitigate(v, player, now);
            case ACTION_UNMITIGATE -> platform.onlineName(player) == null
                    ? new Outcome(false, VerdictNotes.PLAYER_OFFLINE)
                    : new Outcome(true, VerdictNotes.NOTHING_TO_UNMITIGATE);
            case ACTION_KICK -> kick(v, player, now);
            default -> ban(v, player, now);
        };
    }

    private String shadowReason(Verdict v) {
        if (!platform.liveMode()) {
            return VerdictNotes.SHADOW_LOCAL;
        }
        if (!platform.cloudEnforcement()) {
            return VerdictNotes.SHADOW_ENFORCEMENT_OFF;
        }
        if (v.getShadow()) {
            return VerdictNotes.SHADOW_VERDICT;
        }
        return null;
    }

    private Outcome mitigate(Verdict v, UUID player, long now) {
        if (!platform.mitigationsSupported()) {
            return new Outcome(false, VerdictNotes.MITIGATIONS_UNSUPPORTED);
        }
        if (!platform.mitigationsEnabled()) {
            return new Outcome(false, VerdictNotes.MITIGATIONS_DISABLED);
        }
        if (platform.onlineName(player) == null) {
            return new Outcome(false, VerdictNotes.PLAYER_OFFLINE);
        }
        if (platform.exempt(player)) {
            return new Outcome(false, VerdictNotes.EXEMPT);
        }
        if (breaker.isOpen()) {
            return new Outcome(false, VerdictNotes.CIRCUIT_OPEN);
        }
        Set<Mitigation> mitigations = EnumSet.noneOf(Mitigation.class);
        for (Mitigation m : v.getMitigationsList()) {
            if (m != Mitigation.MITIGATION_UNSPECIFIED && m != Mitigation.UNRECOGNIZED) {
                mitigations.add(m);
            }
        }
        if (mitigations.isEmpty()) {
            return new Outcome(false, VerdictNotes.NO_MITIGATIONS);
        }
        double strength = Double.isFinite(v.getStrength()) ? Math.max(0, Math.min(1, v.getStrength())) : 0;
        if (!record(v, player, null, now)) {
            return new Outcome(false, VerdictNotes.NOT_RECORDED);
        }
        if (!platform.mitigate(player, mitigations, strength, v.getVerdictId())) {
            store.rollback(v.getVerdictId());
            return new Outcome(false, platform.onlineName(player) == null
                    ? VerdictNotes.PLAYER_OFFLINE : VerdictNotes.ENFORCEMENT_CHANGED);
        }
        return new Outcome(true, VerdictNotes.MITIGATED);
    }

    private Outcome kick(Verdict v, UUID player, long now) {
        String name = platform.onlineName(player);
        if (name == null) {
            return new Outcome(false, VerdictNotes.PLAYER_OFFLINE);
        }
        if (!Placeholders.validName(name)) {
            return new Outcome(false, VerdictNotes.UNKNOWN_PLAYER);
        }
        if (platform.exempt(player)) {
            return new Outcome(false, VerdictNotes.EXEMPT);
        }
        PunishmentSettings punishments = platform.punishments();
        Placeholders.Values values = values(v, name, player, punishments);
        CircuitBreaker.Result admitted = breaker.acquire(CircuitBreaker.Kind.KICK, now);
        if (admitted != CircuitBreaker.Result.ADMITTED) {
            return circuitOpen(admitted, now);
        }
        if (!record(v, player, null, now)) {
            breaker.release(CircuitBreaker.Kind.KICK, now);
            return new Outcome(false, VerdictNotes.NOT_RECORDED);
        }
        boolean done;
        if (punishments.kickCommand().isEmpty()) {
            String message = Placeholders.expand(punishments.kickMessage(), values);
            done = platform.kick(player, message.isEmpty() ? DEFAULT_KICK_MESSAGE : message);
        } else {
            done = platform.dispatch(Placeholders.expand(punishments.kickCommand(), values));
        }
        if (!done) {
            store.rollback(v.getVerdictId());
            breaker.release(CircuitBreaker.Kind.KICK, now);
            return new Outcome(false, platform.onlineName(player) == null
                    ? VerdictNotes.PLAYER_OFFLINE : VerdictNotes.ENFORCEMENT_CHANGED);
        }
        return new Outcome(true, VerdictNotes.KICKED);
    }

    private Outcome ban(Verdict v, UUID player, long now) {
        String name = resolveName(player);
        if (name == null) {
            return new Outcome(false, VerdictNotes.UNKNOWN_PLAYER);
        }
        if (platform.onlineName(player) != null && platform.exempt(player)) {
            return new Outcome(false, VerdictNotes.EXEMPT);
        }
        PunishmentSettings punishments = platform.punishments();
        if (punishments.banCommand().isEmpty()) {
            return new Outcome(false, VerdictNotes.NO_BAN_COMMAND);
        }
        String command = Placeholders.expand(punishments.banCommand(), values(v, name, player, punishments));
        CircuitBreaker.Result admitted = breaker.acquire(CircuitBreaker.Kind.BAN, now);
        if (admitted != CircuitBreaker.Result.ADMITTED) {
            return circuitOpen(admitted, now);
        }
        if (!record(v, player, new ExecutedStore.Ban(v.getVerdictId(), player, name, now), now)) {
            breaker.release(CircuitBreaker.Kind.BAN, now);
            return new Outcome(false, VerdictNotes.NOT_RECORDED);
        }
        if (!platform.dispatch(command)) {
            store.rollback(v.getVerdictId());
            breaker.release(CircuitBreaker.Kind.BAN, now);
            return new Outcome(false, VerdictNotes.ENFORCEMENT_CHANGED);
        }
        return new Outcome(true, VerdictNotes.BANNED);
    }

    private Outcome unban(Verdict v, UUID player, long now) {
        ExecutedStore.Ban ban = store.ban(v.getSupersedes());
        store.noteUnban(player, v.getCreatedUnixMs(), now);
        if (ban == null) {
            return new Outcome(false, VerdictNotes.NOT_EXECUTED_HERE);
        }
        if (!ban.player().equals(player)) {
            store.noteUnban(ban.player(), v.getCreatedUnixMs(), now);
        }
        String name = Placeholders.validName(ban.name()) ? ban.name() : resolveName(ban.player());
        if (name == null) {
            return new Outcome(false, VerdictNotes.UNKNOWN_PLAYER);
        }
        PunishmentSettings punishments = platform.punishments();
        if (punishments.unbanCommand().isEmpty()) {
            return new Outcome(false, VerdictNotes.NO_UNBAN_COMMAND);
        }
        String command = Placeholders.expand(punishments.unbanCommand(), values(v, name, ban.player(), punishments));
        if (!record(v, ban.player(), null, now)) {
            return new Outcome(false, VerdictNotes.NOT_RECORDED);
        }
        if (!platform.dispatchUnban(command)) {
            store.rollback(v.getVerdictId());
            return new Outcome(false, VerdictNotes.ENFORCEMENT_CHANGED);
        }
        store.removeBan(ban.verdictId());
        return new Outcome(true, VerdictNotes.UNBANNED);
    }

    private Outcome circuitOpen(CircuitBreaker.Result result, long now) {
        if (result == CircuitBreaker.Result.TRIPPED) {
            CircuitBreaker.Status status = breaker.status(now);
            store.breaker(new ExecutedStore.BreakerState(true, status.openSinceUnixMs(), status.reason()));
            platform.urgent("punishment circuit breaker OPEN (" + status.reason() + "): PandaEye stops kicking,"
                    + " banning and mitigating until an admin runs /pandaeye breaker reset");
        }
        return new Outcome(false, VerdictNotes.CIRCUIT_OPEN);
    }

    private boolean record(Verdict v, UUID player, ExecutedStore.Ban ban, long now) {
        try {
            store.recordExecuted(new ExecutedStore.Executed(v.getVerdictId(), actionName(v.getAction()), player, now),
                    ban, now);
            return true;
        } catch (IOException e) {
            log.warn("could not record verdict " + Placeholders.token(v.getVerdictId(), 64) + " in "
                    + store.file().getFileName() + ", so it is not executed: " + e);
            return false;
        }
    }

    private void recordQuietly(Verdict v, UUID player, long now) {
        record(v, player, null, now);
    }

    /** Online name, else the offline cache; null unless it is a valid name. */
    private String resolveName(UUID player) {
        String online = platform.onlineName(player);
        if (online != null) {
            return Placeholders.validName(online) ? online : null;
        }
        String cached = platform.cachedName(player);
        return Placeholders.validName(cached) ? cached : null;
    }

    private String displayName(UUID player, Verdict v) {
        try {
            String local = resolveName(player);
            if (local != null) {
                return local;
            }
        } catch (RuntimeException ignored) {
            // fall through to the cloud's name, sanitised
        }
        String fromCloud = v.getPlayerName();
        return Placeholders.validName(fromCloud) ? fromCloud + "?" : player.toString();
    }

    private static Placeholders.Values values(Verdict v, String name, UUID player, PunishmentSettings punishments) {
        return new Placeholders.Values(name, player, v.getReason(), String.join(", ", v.getChecksList()),
                v.getVerdictId(), v.getAppealCode(), punishments.appealUrl(), v.getBanDurationS());
    }

    private void announce(Verdict v, String name, Outcome outcome) {
        String text = String.format(Locale.ROOT, "verdict %s for %s (score %.1f, confidence %.2f%s%s): %s - %s",
                actionName(v.getAction()), name, v.getScore(), v.getConfidence(),
                v.getChecksCount() == 0 ? "" : ", " + Placeholders.text(String.join(", ", v.getChecksList())),
                v.getShadow() ? ", shadow" : "", outcome.executed() ? "executed" : "not executed",
                outcome.note());
        platform.alert("verdict|" + Placeholders.token(v.getVerdictId(), 64), text);
    }

    static String actionName(Action action) {
        return action == Action.UNRECOGNIZED ? "UNRECOGNIZED" : action.name().replace("ACTION_", "");
    }
}
