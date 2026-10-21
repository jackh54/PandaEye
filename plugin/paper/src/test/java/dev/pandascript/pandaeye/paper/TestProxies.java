package dev.pandascript.pandaeye.paper;

import java.lang.reflect.Proxy;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * Interface stand-ins for Bukkit types (Player, Block, ...) without a server: named methods return the given
 * values, everything else returns a type default (false, 0, null). Equality is identity.
 */
public final class TestProxies {

    private TestProxies() {
    }

    public static <T> T of(Class<T> type, Map<String, Object> returns) {
        return of(type, (name, args) -> returns.get(name));
    }

    @SuppressWarnings("unchecked")
    public static <T> T of(Class<T> type, BiFunction<String, Object[], Object> answers) {
        Object[] self = new Object[1];
        self[0] = Proxy.newProxyInstance(type.getClassLoader(), new Class<?>[]{type}, (proxy, method, args) -> {
            switch (method.getName()) {
                case "equals" -> {
                    return args[0] == proxy;
                }
                case "hashCode" -> {
                    return System.identityHashCode(proxy);
                }
                case "toString" -> {
                    return type.getSimpleName() + "@test";
                }
                default -> {
                }
            }
            Object value = answers.apply(method.getName(), args);
            if (value != null) {
                return value;
            }
            Class<?> r = method.getReturnType();
            if (r == boolean.class) {
                return false;
            }
            if (r == int.class) {
                return 0;
            }
            if (r == long.class) {
                return 0L;
            }
            if (r == double.class) {
                return 0.0;
            }
            if (r == float.class) {
                return 0f;
            }
            if (r == short.class) {
                return (short) 0;
            }
            if (r == byte.class) {
                return (byte) 0;
            }
            if (r == char.class) {
                return (char) 0;
            }
            return null;
        });
        return (T) self[0];
    }
}
