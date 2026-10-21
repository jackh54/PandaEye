package dev.pandascript.pandaeye.paper.check;

/**
 * The check-specific part of the confirmation step (CHECKS 1.1), run one tick after the candidate on the
 * player's entity thread, after the world/region/chunk guards and the common authoritative exemptions passed.
 * Must be fast and must not keep references to the context.
 */
@FunctionalInterface
public interface Confirmer {

    /** Confirms nothing beyond the common guards. */
    Confirmer NONE = context -> Confirmation.CONFIRMED;

    Confirmation confirm(ConfirmContext context);
}
