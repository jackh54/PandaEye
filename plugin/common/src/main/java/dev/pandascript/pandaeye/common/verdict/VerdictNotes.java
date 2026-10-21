package dev.pandascript.pandaeye.common.verdict;

/**
 * The exact {@code VerdictAck.note} values the plugins send (ARCHITECTURE 4.7, PROTOCOL 7.2). Notes of acks
 * with {@code executed = false} name the reason; the dashboard shows them verbatim.
 */
public final class VerdictNotes {

    // Not executed: shadow (local mode, last-known RemoteConfig.enforcement, verdict.shadow).
    public static final String SHADOW_LOCAL = "shadow: local mode";
    public static final String SHADOW_ENFORCEMENT_OFF = "shadow: enforcement off";
    public static final String SHADOW_VERDICT = "shadow: verdict";

    // Not executed: stale or superseded.
    public static final String STALE_KICK = "stale: kick older than 5 min";
    public static final String STALE_BAN = "stale: ban older than 24 h";
    public static final String BAN_BEFORE_UNBAN = "superseded: ban older than the player's last unban";

    // Not executed: player.
    public static final String UNKNOWN_PLAYER = "unknown player";
    public static final String PLAYER_OFFLINE = "player offline";
    public static final String EXEMPT = "exempt: bypass permission";

    // Not executed: safety and configuration.
    public static final String CIRCUIT_OPEN = "circuit-open";
    public static final String NOT_EXECUTED_HERE = "not executed here";
    public static final String NO_BAN_COMMAND = "not executed: no ban-command configured";
    public static final String NO_UNBAN_COMMAND = "not executed: no unban-command configured";
    public static final String MITIGATIONS_DISABLED = "not executed: mitigations disabled locally";
    public static final String MITIGATIONS_UNSUPPORTED = "not executed: mitigations are not supported here";
    public static final String NO_MITIGATIONS = "not executed: no mitigations in verdict";
    public static final String NOT_RECORDED = "not executed: could not record the execution";
    public static final String ENFORCEMENT_CHANGED = "not executed: enforcement turned off";
    public static final String UNSUPPORTED_ACTION = "not executed: unsupported action";
    public static final String INVALID = "not executed: invalid verdict";

    // Executed.
    public static final String ALERTED = "alerted";
    public static final String MITIGATED = "mitigated";
    public static final String UNMITIGATED = "unmitigated";
    public static final String NOTHING_TO_UNMITIGATE = "no active mitigations";
    public static final String KICKED = "kicked";
    public static final String BANNED = "banned";
    public static final String UNBANNED = "unbanned";
    public static final String ALREADY_EXECUTED = "already executed";

    private VerdictNotes() {
    }
}
