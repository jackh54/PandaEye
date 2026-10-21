package dev.pandascript.pandaeye.paper.session;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.util.UUID;

/** Bedrock players: Floodgate's API through reflection when present, else the UUID heuristic (msb == 0). */
public final class BedrockDetector {

    private final Object api;
    private final MethodHandle isFloodgatePlayer;

    private BedrockDetector(Object api, MethodHandle isFloodgatePlayer) {
        this.api = api;
        this.isFloodgatePlayer = isFloodgatePlayer;
    }

    public static BedrockDetector detect(ClassLoader pluginLoader) {
        try {
            Class<?> type = Class.forName("org.geysermc.floodgate.api.FloodgateApi", true, pluginLoader);
            Object api = type.getMethod("getInstance").invoke(null);
            MethodHandle handle = MethodHandles.publicLookup().findVirtual(type, "isFloodgatePlayer",
                    MethodType.methodType(boolean.class, UUID.class));
            return new BedrockDetector(api, handle);
        } catch (ReflectiveOperationException | LinkageError | RuntimeException e) {
            return new BedrockDetector(null, null);
        }
    }

    public boolean floodgate() {
        return api != null;
    }

    public boolean isBedrock(UUID player) {
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
