package dev.pandascript.pandaeye.paper.check;

import java.util.List;

/**
 * What a {@link Confirmer} gets on the player's entity thread: world access and the positions the candidate
 * was computed from (oldest first, e.g. n-2, n-1, n for movement.speed).
 */
public interface ConfirmContext {

    WorldView world();

    List<Position> positions();

    /** Current authoritative player scale attribute (1.0 default). */
    double scale();

    /** A position used by a candidate. */
    record Position(double x, double y, double z) {
    }
}
