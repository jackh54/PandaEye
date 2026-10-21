package dev.pandascript.pandaeye.paper.check;

/** The outcome of a {@link Confirmer}. */
public sealed interface Confirmation {

    /** Confirmed with the candidate's violation unchanged. */
    Confirmation CONFIRMED = new Confirmed(null);

    static Confirmation confirmed(Violation updated) {
        return new Confirmed(updated);
    }

    static Confirmation discarded(String reason) {
        return new Discarded(reason);
    }

    /** {@code violation} replaces the candidate's (e.g. severity recomputed with real friction); null keeps it. */
    record Confirmed(Violation violation) implements Confirmation {
    }

    record Discarded(String reason) implements Confirmation {
    }
}
