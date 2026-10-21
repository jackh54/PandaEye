package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.Move;

import java.util.List;
import java.util.UUID;

/**
 * A violation that needs the world-state confirmation step before it counts.
 *
 * @param positions  the positions the check used, oldest first; all must be in loaded chunks owned by the
 *                   player's current region
 * @param movement   apply the common authoritative movement exemptions (riptide, gliding, flying, vehicle,
 *                   recent PlayerVelocityEvent, game mode) before the confirmer
 * @param worldUid   the world the player was in when the candidate was raised
 * @param context    the player's last movement packets at the time (LocalFlag context)
 */
public record Candidate(
        Violation violation,
        List<ConfirmContext.Position> positions,
        boolean movement,
        Confirmer confirmer,
        UUID worldUid,
        int epoch,
        List<Move> context,
        long nanos) {

    public Candidate {
        positions = List.copyOf(positions);
        context = List.copyOf(context);
    }
}
