package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.paper.TestProxies;
import dev.pandascript.pandaeye.paper.session.MitigationState;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import org.bukkit.block.Block;
import org.bukkit.block.BlockState;
import org.bukkit.damage.DamageSource;
import org.bukkit.entity.Entity;
import org.bukkit.entity.Player;
import org.bukkit.event.block.BlockPlaceEvent;
import org.bukkit.event.entity.EntityDamageByEntityEvent;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.inventory.EquipmentSlot;
import org.bukkit.util.Vector;
import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.Set;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Mitigation math (ARCHITECTURE 4.8) and the listener that applies it through the live enforcer. */
class MitigationListenerTest {

    private final Player mitigated = TestProxies.of(Player.class, Map.of("getUniqueId", UUID.randomUUID()));
    private final Player other = TestProxies.of(Player.class, Map.of("getUniqueId", UUID.randomUUID()));
    private final Entity victim = TestProxies.of(Entity.class, Map.of());
    private MitigationState state = MitigationState.NONE;
    private double nextRandom;
    private final LiveEnforcer live = new LiveEnforcer(null, task -> {
        throw new AssertionError("no command expected");
    });
    private final MitigationListener listener = new MitigationListener(live, new MitigationSource() {
        @Override
        public MitigationState state(Player player) {
            return player == mitigated ? state : null;
        }

        @Override
        public void failed(String site, Throwable error) {
            throw new AssertionError(site, error);
        }
    }, () -> nextRandom);

    @Test
    void formulas() {
        assertEquals(0.15, MitigationMath.hitCancelProbability(0), 1e-12);
        assertEquals(0.50, MitigationMath.hitCancelProbability(1), 1e-12);
        assertEquals(0.325, MitigationMath.hitCancelProbability(0.5), 1e-12);
        assertEquals(1.0, MitigationMath.damageFactor(0), 1e-12);
        assertEquals(0.7, MitigationMath.damageFactor(1), 1e-12);
        assertEquals(0.85, MitigationMath.damageFactor(0.5), 1e-12);
        assertEquals(1.0, MitigationMath.velocityFactor(0), 1e-12);
        assertEquals(1.25, MitigationMath.velocityFactor(1), 1e-12);
        assertEquals(0.2, MitigationMath.placeCancelProbability(0), 1e-12);
        assertEquals(0.6, MitigationMath.placeCancelProbability(1), 1e-12);
        assertEquals(0.4, MitigationMath.placeCancelProbability(0.5), 1e-12);
        assertEquals(0.7, MitigationMath.damageFactor(5), 1e-12, "strength is clamped");
        assertEquals(1.0, MitigationMath.damageFactor(-2), 1e-12);
        assertEquals(0.15, MitigationMath.hitCancelProbability(Double.NaN), 1e-12);
        assertTrue(MitigationMath.roll(0.3, 0.29));
        assertFalse(MitigationMath.roll(0.3, 0.3));
    }

    @SuppressWarnings("removal")
    private EntityDamageByEntityEvent hit(Entity damager, double damage) {
        return new EntityDamageByEntityEvent(damager, victim, EntityDamageEvent.DamageCause.ENTITY_ATTACK,
                TestProxies.of(DamageSource.class, Map.of()), damage);
    }

    @Test
    void hitCancelUsesTheStrengthProbability() {
        state = new MitigationState(Set.of(Mitigation.MITIGATION_HIT_CANCEL), 1.0, "v");
        nextRandom = 0.49;
        EntityDamageByEntityEvent cancelled = hit(mitigated, 6);
        listener.onDamage(cancelled);
        assertTrue(cancelled.isCancelled());
        nextRandom = 0.5;
        EntityDamageByEntityEvent kept = hit(mitigated, 6);
        listener.onDamage(kept);
        assertFalse(kept.isCancelled());
        assertEquals(1, live.counters().get(EnforcerCounters.Action.CANCEL));
    }

    @Test
    void damageReduceScalesTheDamage() {
        state = new MitigationState(Set.of(Mitigation.MITIGATION_HIT_CANCEL, Mitigation.MITIGATION_DAMAGE_REDUCE), 0.5, "v");
        nextRandom = 0.99;
        EntityDamageByEntityEvent event = hit(mitigated, 10);
        listener.onDamage(event);
        assertFalse(event.isCancelled());
        assertEquals(8.5, event.getDamage(), 1e-9);
    }

    @Test
    void onlyTheMitigatedDamagerIsAffected() {
        state = new MitigationState(Set.of(Mitigation.MITIGATION_HIT_CANCEL, Mitigation.MITIGATION_DAMAGE_REDUCE), 1, "v");
        nextRandom = 0;
        EntityDamageByEntityEvent byOther = hit(other, 10);
        listener.onDamage(byOther);
        EntityDamageByEntityEvent byEntity = hit(victim, 10);
        listener.onDamage(byEntity);
        assertFalse(byOther.isCancelled());
        assertEquals(10, byOther.getDamage(), 1e-9);
        assertFalse(byEntity.isCancelled());
        assertEquals(0, live.counters().total());
    }

    @Test
    void velocityBoostMultipliesTheVelocity() {
        state = new MitigationState(Set.of(Mitigation.MITIGATION_VELOCITY_BOOST), 1.0, "v");
        PlayerVelocityEvent event = new PlayerVelocityEvent(mitigated, new Vector(0.4, 0.36, -0.2));
        listener.onVelocity(event);
        assertEquals(0.5, event.getVelocity().getX(), 1e-9);
        assertEquals(0.45, event.getVelocity().getY(), 1e-9);
        assertEquals(-0.25, event.getVelocity().getZ(), 1e-9);
        PlayerVelocityEvent untouched = new PlayerVelocityEvent(other, new Vector(0.4, 0.36, -0.2));
        listener.onVelocity(untouched);
        assertEquals(0.4, untouched.getVelocity().getX(), 1e-9);
    }

    @Test
    void placeCancelUsesTheStrengthProbability() {
        state = new MitigationState(Set.of(Mitigation.MITIGATION_PLACE_CANCEL), 0.5, "v");
        Block block = TestProxies.of(Block.class, Map.of());
        BlockState replaced = TestProxies.of(BlockState.class, Map.of());
        nextRandom = 0.39;
        BlockPlaceEvent cancelled = new BlockPlaceEvent(block, replaced, block, null, mitigated, true, EquipmentSlot.HAND);
        listener.onPlace(cancelled);
        assertTrue(cancelled.isCancelled());
        nextRandom = 0.4;
        BlockPlaceEvent kept = new BlockPlaceEvent(block, replaced, block, null, mitigated, true, EquipmentSlot.HAND);
        listener.onPlace(kept);
        assertFalse(kept.isCancelled());
    }

    @Test
    void noStateMeansNoChange() {
        state = null;
        nextRandom = 0;
        EntityDamageByEntityEvent event = hit(mitigated, 4);
        listener.onDamage(event);
        PlayerVelocityEvent velocity = new PlayerVelocityEvent(mitigated, new Vector(1, 1, 1));
        listener.onVelocity(velocity);
        assertFalse(event.isCancelled());
        assertEquals(4, event.getDamage(), 1e-9);
        assertEquals(1, velocity.getVelocity().getX(), 1e-9);
        assertEquals(0, live.counters().total());
    }
}
