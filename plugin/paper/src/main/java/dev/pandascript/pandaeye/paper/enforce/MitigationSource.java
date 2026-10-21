package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.paper.session.MitigationState;
import org.bukkit.entity.Player;

/**
 * Tells the mitigation listener which mitigations apply to a player right now. Implementations re-check that
 * enforcement is still allowed (local mode live, cloud enforcement on, {@code mitigations.enabled}) on every
 * call, so a listener that is still registered for a moment after enforcement was switched off does nothing.
 */
public interface MitigationSource {

    /** Event thread; null (or an inactive state) when nothing applies. Must be cheap and never throw. */
    MitigationState state(Player player);

    /** A listener callback failed; the event is left as it was. */
    void failed(String site, Throwable error);
}
