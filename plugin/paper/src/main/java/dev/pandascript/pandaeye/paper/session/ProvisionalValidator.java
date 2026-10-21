package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.paper.platform.Schedulers;
import org.bukkit.Material;
import org.bukkit.enchantments.Enchantment;
import org.bukkit.entity.Player;
import org.bukkit.inventory.ItemStack;

import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.BooleanSupplier;

/**
 * Validates provisional riptide/elytra exemptions against authoritative state a few ticks later (CHECKS 1.1):
 * riptiding (or a riptide trident in hand while in water or rain), or gliding. Failures feed the session's
 * {@link ProvisionalGuard}. At most one pending validation per player and kind.
 */
public final class ProvisionalValidator {

    private static final long DELAY_TICKS = 3;

    private final Schedulers schedulers;
    private final BooleanSupplier active;
    private final Set<String> pending = ConcurrentHashMap.newKeySet();

    public ProvisionalValidator(Schedulers schedulers, BooleanSupplier active) {
        this.schedulers = schedulers;
        this.active = active;
    }

    public void validate(Session session, boolean riptide) {
        Player player = session.player();
        String key = session.uuid() + (riptide ? ":riptide" : ":elytra");
        if (player == null || !pending.add(key)) {
            return;
        }
        boolean scheduled = schedulers.runOnEntity(player, session.uuid(), task -> {
            pending.remove(key);
            if (active.getAsBoolean() && !session.closed() && !valid(player, riptide)) {
                session.signals().provisionalFailed(System.nanoTime());
            }
        }, () -> pending.remove(key), DELAY_TICKS);
        if (!scheduled) {
            pending.remove(key);
        }
    }

    private static boolean valid(Player player, boolean riptide) {
        if (!riptide) {
            return player.isGliding();
        }
        return player.isRiptiding() || (player.isInWaterOrRain()
                && (riptideTrident(player.getInventory().getItemInMainHand())
                || riptideTrident(player.getInventory().getItemInOffHand())));
    }

    private static boolean riptideTrident(ItemStack item) {
        return item != null && item.getType() == Material.TRIDENT && item.containsEnchantment(Enchantment.RIPTIDE);
    }
}
