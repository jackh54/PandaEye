package dev.pandascript.pandaeye.paper.enforce;

/** The mitigation formulas of ARCHITECTURE 4.8; strength is clamped to 0..1 (NaN = 0). */
public final class MitigationMath {

    private MitigationMath() {
    }

    public static double clamp(double strength) {
        return Double.isFinite(strength) ? Math.max(0, Math.min(1, strength)) : 0;
    }

    /** HIT_CANCEL: probability that a melee hit of the mitigated player is cancelled (0.15 .. 0.5). */
    public static double hitCancelProbability(double strength) {
        return 0.15 + 0.35 * clamp(strength);
    }

    /** DAMAGE_REDUCE: factor applied to the damage the mitigated player deals (1 .. 0.7). */
    public static double damageFactor(double strength) {
        return 1 - 0.3 * clamp(strength);
    }

    /** VELOCITY_BOOST: factor applied to the velocity (knockback) the mitigated player takes (1 .. 1.25). */
    public static double velocityFactor(double strength) {
        return 1 + 0.25 * clamp(strength);
    }

    /** PLACE_CANCEL: probability that a block placement of the mitigated player is cancelled (0.2 .. 0.6). */
    public static double placeCancelProbability(double strength) {
        return 0.2 + 0.4 * clamp(strength);
    }

    /** One roll: {@code random} in [0, 1) hits when it is below the probability. */
    public static boolean roll(double probability, double random) {
        return random < probability;
    }
}
