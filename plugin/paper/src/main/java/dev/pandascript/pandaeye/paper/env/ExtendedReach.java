package dev.pandascript.pandaeye.paper.env;

import org.bukkit.inventory.ItemStack;

import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.util.Locale;

/**
 * Detects items with a custom attack range (spears, the {@code attack_range} data component of newer
 * versions) through reflection, so the plugin still loads on an API without them (false then).
 */
public final class ExtendedReach {

    private final Object attackRangeType;
    private final Method hasData;

    private ExtendedReach(Object attackRangeType, Method hasData) {
        this.attackRangeType = attackRangeType;
        this.hasData = hasData;
    }

    public static ExtendedReach detect() {
        try {
            Class<?> types = Class.forName("io.papermc.paper.datacomponent.DataComponentTypes");
            Field field = types.getField("ATTACK_RANGE");
            Class<?> typeClass = Class.forName("io.papermc.paper.datacomponent.DataComponentType");
            return new ExtendedReach(field.get(null), ItemStack.class.getMethod("hasData", typeClass));
        } catch (ReflectiveOperationException | LinkageError e) {
            return new ExtendedReach(null, null);
        }
    }

    public boolean test(ItemStack item) {
        if (item == null || item.getType().isAir()) {
            return false;
        }
        if (item.getType().name().toLowerCase(Locale.ROOT).endsWith("_spear")) {
            return true;
        }
        if (attackRangeType == null) {
            return false;
        }
        try {
            return (Boolean) hasData.invoke(item, attackRangeType);
        } catch (ReflectiveOperationException | RuntimeException e) {
            return false;
        }
    }
}
