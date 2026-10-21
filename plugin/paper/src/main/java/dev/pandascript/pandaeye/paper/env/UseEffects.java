package dev.pandascript.pandaeye.paper.env;

import org.bukkit.inventory.ItemStack;

import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.util.Locale;

/**
 * The movement multiplier while using an item: vanilla scales movement input by 0.2, and 1.21.11 added the
 * {@code use_effects} data component ({@code speed_multiplier}, spears use it to move at full speed while
 * charging). Read through reflection so the plugin still loads on an API without it; on such a server the
 * component does not exist and every item slows by 0.2.
 */
public final class UseEffects {

    public static final double DEFAULT_MULTIPLIER = 0.2;

    private final Object type;
    private final Method hasData;
    private final Method getData;
    private final Method speedMultiplier;

    private UseEffects(Object type, Method hasData, Method getData, Method speedMultiplier) {
        this.type = type;
        this.hasData = hasData;
        this.getData = getData;
        this.speedMultiplier = speedMultiplier;
    }

    public static UseEffects detect() {
        try {
            Class<?> types = Class.forName("io.papermc.paper.datacomponent.DataComponentTypes");
            Field field = types.getField("USE_EFFECTS");
            Class<?> typeClass = Class.forName("io.papermc.paper.datacomponent.DataComponentType");
            Class<?> valuedClass = Class.forName("io.papermc.paper.datacomponent.DataComponentType$Valued");
            return new UseEffects(field.get(null), ItemStack.class.getMethod("hasData", typeClass),
                    ItemStack.class.getMethod("getData", valuedClass), speedMultiplierAccessor());
        } catch (ReflectiveOperationException | LinkageError e) {
            return new UseEffects(null, null, null, null);
        }
    }

    /** The component's API accessor; null when it has another shape (items with the component are then unknown). */
    private static Method speedMultiplierAccessor() {
        try {
            return Class.forName("io.papermc.paper.datacomponent.item.UseEffects").getMethod("speedMultiplier");
        } catch (ReflectiveOperationException | LinkageError e) {
            return null;
        }
    }

    /** The item's multiplier, NaN when it cannot be determined (treated as "does not slow"). */
    public double multiplier(ItemStack item) {
        if (item == null || item.getType().isAir()) {
            return Double.NaN;
        }
        if (item.getType().name().toLowerCase(Locale.ROOT).endsWith("_spear")) {
            return Double.NaN;
        }
        if (type == null) {
            return DEFAULT_MULTIPLIER;
        }
        try {
            if (!(Boolean) hasData.invoke(item, type)) {
                return DEFAULT_MULTIPLIER;
            }
            Object component = getData.invoke(item, type);
            if (component == null) {
                return DEFAULT_MULTIPLIER;
            }
            if (speedMultiplier == null) {
                return Double.NaN;
            }
            return speedMultiplier.invoke(component) instanceof Number number ? number.doubleValue() : Double.NaN;
        } catch (ReflectiveOperationException | RuntimeException e) {
            return Double.NaN;
        }
    }
}
