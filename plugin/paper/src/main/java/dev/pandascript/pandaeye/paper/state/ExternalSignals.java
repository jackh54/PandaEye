package dev.pandascript.pandaeye.paper.state;

/**
 * Inputs to the packet state that come from other threads (environment sampler, permission checks,
 * provisional-exemption validation). Implementations read volatile or lock-free data only.
 */
public interface ExternalSignals {

    /** Bedrock (when exempt), or the bypass permission outside shadow mode (unknown counts as exempt there). */
    boolean detectionExempt();

    /** The latest environment sample, or null before the first one. */
    EnvSnapshot env();

    /** True when any flag of {@code envMask} was set in a sample within {@code windowNanos} before {@code nowNanos}. */
    boolean envRecent(int envMask, long windowNanos, long nowNanos);

    /** False while the player is barred from provisional riptide/elytra exemptions (CHECKS 1.1). */
    boolean provisionalExemptionsAllowed(long nowNanos);
}
