package dev.pandascript.pandaeye.velocity;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.util.UUID;

/** Bedrock players: Floodgate's API through reflection when present, else the UUID heuristic (msb == 0). */
final class BedrockCheck {

    private final Object api;
    private final MethodHandle isFloodgatePlayer;

    private BedrockCheck(Object api, MethodHandle isFloodgatePlayer) {
        this.api = api;
        this.isFloodgatePlayer = isFloodgatePlayer;
    }

    static BedrockCheck detect(ClassLoader loader) {
        try {
            Class<?> type = Class.forName("org.geysermc.floodgate.api.FloodgateApi", true, loader);
            Object api = type.getMethod("getInstance").invoke(null);
            MethodHandle handle = MethodHandles.publicLookup().findVirtual(type, "isFloodgatePlayer",
                    MethodType.methodType(boolean.class, UUID.class));
            return new BedrockCheck(api, handle);
        } catch (ReflectiveOperationException | LinkageError | RuntimeException e) {
            return new BedrockCheck(null, null);
        }
    }

    boolean floodgate() {
        return api != null;
    }

    boolean isBedrock(UUID player) {
        if (player.getMostSignificantBits() == 0) {
            return true;
        }
        if (api == null) {
            return false;
        }
        try {
            return (boolean) isFloodgatePlayer.invoke(api, player);
        } catch (Throwable t) {
            return false;
        }
    }
}
