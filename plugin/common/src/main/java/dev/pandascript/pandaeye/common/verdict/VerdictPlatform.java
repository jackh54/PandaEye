package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.protocol.v1.Mitigation;

import java.util.Set;
import java.util.UUID;

/**
 * What {@link VerdictProcessor} needs from a platform (Paper/Folia or Velocity). Queries are called on the
 * cloud thread and must be thread-safe and non-blocking. Side-effect methods go through the platform's
 * enforcer (which schedules onto the right thread) and return false when nothing was done (player gone,
 * enforcement switched off in the meantime); they are only called after every gate of ARCHITECTURE 4.7
 * passed, except {@link #dispatchUnban}, which is the documented any-mode exception.
 */
public interface VerdictPlatform {

    /** Local {@code mode: live}. */
    boolean liveMode();

    /** The last-known {@code RemoteConfig.enforcement} (false until the cloud has ever said otherwise). */
    boolean cloudEnforcement();

    PunishmentSettings punishments();

    /** Whether MITIGATE can be applied on this platform at all. */
    boolean mitigationsSupported();

    /** The local {@code mitigations.enabled} switch. */
    boolean mitigationsEnabled();

    long nowUnixMillis();

    /** The player's name when online here, else null. */
    String onlineName(UUID player);

    /** The platform's offline player cache, else null. */
    String cachedName(UUID player);

    /** True when an online player must not be punished (bypass permission, or not evaluated yet). */
    boolean exempt(UUID player);

    /** {@code player.kick(message)} on the player's thread. */
    boolean kick(UUID player, String message);

    /** Runs a console command (global region thread on Folia). */
    boolean dispatch(String command);

    /** Runs the unban command for a ban this server executed, in any mode. */
    boolean dispatchUnban(String command);

    boolean mitigate(UUID player, Set<Mitigation> mitigations, double strength, String verdictId);

    /** Clears the player's mitigations; true when some were active. */
    boolean unmitigate(UUID player);

    /** Staff + console alert, throttled per key. */
    void alert(String key, String text);

    /** An alert that must reach the console (circuit breaker), plus staff. */
    void urgent(String text);

    /** The local verdict log (optional). */
    default void logged(VerdictProcessor.Logged entry) {
    }
}
