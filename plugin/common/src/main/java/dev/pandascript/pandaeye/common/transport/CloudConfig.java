package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.protocol.v1.Platform;

import java.net.URI;
import java.nio.file.Path;
import java.time.Duration;
import java.util.List;

/**
 * Everything the {@link CloudClient} needs from the platform. The endpoint must already have passed
 * {@link EndpointPolicy}.
 *
 * @param dataDir the plugin data folder ({@code server-id}, {@code state.properties}, {@code spool/}, ...)
 */
public record CloudConfig(
        URI endpoint,
        String licenseKey,
        String serverName,
        String networkId,
        Platform platform,
        String platformVersion,
        String pluginVersion,
        String minecraftVersion,
        boolean shadowMode,
        List<String> capabilities,
        Duration batchInterval,
        long maxBufferBytes,
        Duration maxBufferAge,
        Path dataDir) {

    private static final int MAX_STRING = 256;

    public CloudConfig {
        serverName = clip(serverName);
        networkId = clip(networkId);
        platformVersion = clip(platformVersion);
        pluginVersion = clip(pluginVersion);
        minecraftVersion = clip(minecraftVersion);
        capabilities = List.copyOf(capabilities);
        if (batchInterval.isNegative() || batchInterval.isZero()) {
            throw new IllegalArgumentException("batch interval must be positive");
        }
    }

    private static String clip(String value) {
        if (value == null) {
            return "";
        }
        return value.length() <= MAX_STRING ? value : value.substring(0, MAX_STRING);
    }
}
