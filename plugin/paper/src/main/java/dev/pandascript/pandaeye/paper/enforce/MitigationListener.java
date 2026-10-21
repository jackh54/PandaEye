package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.paper.session.MitigationState;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import org.bukkit.entity.Player;
import org.bukkit.event.EventHandler;
import org.bukkit.event.EventPriority;
import org.bukkit.event.Listener;
import org.bukkit.event.block.BlockPlaceEvent;
import org.bukkit.event.entity.EntityDamageByEntityEvent;
import org.bukkit.event.player.PlayerVelocityEvent;

import java.util.function.DoubleSupplier;

/**
 * The mitigation listeners of ARCHITECTURE 4.8. Constructed and registered only by {@link LiveEnforcer} (so only
 * in live mode with enforcement), and every change it makes goes through the live enforcer's methods; both
 * are asserted by {@code EnforcerArchitectureTest}. Runs at HIGH so MONITOR listeners (PandaEye's own
 * read-only velocity tracking included) see the final values; ignores events already cancelled.
 */
public final class MitigationListener implements Listener {

    private final LiveEnforcer live;
    private final MitigationSource source;
    private final DoubleSupplier random;

    MitigationListener(LiveEnforcer live, MitigationSource source, DoubleSupplier random) {
        this.live = live;
        this.source = source;
        this.random = random;
    }

    @EventHandler(priority = EventPriority.HIGH, ignoreCancelled = true)
    public void onDamage(EntityDamageByEntityEvent event) {
        try {
            if (!(event.getDamager() instanceof Player damager)) {
                return;
            }
            MitigationState state = source.state(damager);
            if (state == null || !state.active()) {
                return;
            }
            if (state.has(Mitigation.MITIGATION_HIT_CANCEL)
                    && MitigationMath.roll(MitigationMath.hitCancelProbability(state.strength()), random.getAsDouble())) {
                live.cancel(event);
                return;
            }
            if (state.has(Mitigation.MITIGATION_DAMAGE_REDUCE)) {
                live.setDamage(event, event.getDamage() * MitigationMath.damageFactor(state.strength()));
            }
        } catch (Throwable t) {
            source.failed("mitigation damage", t);
        }
    }

    @EventHandler(priority = EventPriority.HIGH, ignoreCancelled = true)
    public void onVelocity(PlayerVelocityEvent event) {
        try {
            MitigationState state = source.state(event.getPlayer());
            if (state != null && state.has(Mitigation.MITIGATION_VELOCITY_BOOST)) {
                live.setVelocity(event, event.getVelocity().clone()
                        .multiply(MitigationMath.velocityFactor(state.strength())));
            }
        } catch (Throwable t) {
            source.failed("mitigation velocity", t);
        }
    }

    @EventHandler(priority = EventPriority.HIGH, ignoreCancelled = true)
    public void onPlace(BlockPlaceEvent event) {
        try {
            MitigationState state = source.state(event.getPlayer());
            if (state != null && state.has(Mitigation.MITIGATION_PLACE_CANCEL)
                    && MitigationMath.roll(MitigationMath.placeCancelProbability(state.strength()), random.getAsDouble())) {
                live.cancel(event);
            }
        } catch (Throwable t) {
            source.failed("mitigation place", t);
        }
    }
}
