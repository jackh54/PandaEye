package dev.pandascript.pandaeye.paper.state;

import java.util.List;

/** Vanilla attribute value computation (base, then additions, then multipliers). */
public final class AttributeMath {

    public static final String SPRINTING_MODIFIER = "minecraft:sprinting";

    public enum Operation {
        ADD_VALUE,
        ADD_MULTIPLIED_BASE,
        ADD_MULTIPLIED_TOTAL
    }

    public record Modifier(Operation operation, double amount) {
    }

    private AttributeMath() {
    }

    public static double value(double base, List<Modifier> modifiers) {
        double value = base;
        for (Modifier modifier : modifiers) {
            if (modifier.operation() == Operation.ADD_VALUE) {
                value += modifier.amount();
            }
        }
        double result = value;
        for (Modifier modifier : modifiers) {
            if (modifier.operation() == Operation.ADD_MULTIPLIED_BASE) {
                result += value * modifier.amount();
            }
        }
        for (Modifier modifier : modifiers) {
            if (modifier.operation() == Operation.ADD_MULTIPLIED_TOTAL) {
                result *= 1 + modifier.amount();
            }
        }
        return result;
    }
}
