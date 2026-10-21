package dev.pandascript.pandaeye.velocity;

import dev.pandascript.pandaeye.common.transport.RemoteSettings;

import java.util.function.BooleanSupplier;
import java.util.function.Supplier;

/**
 * Hands out the live enforcer only when local mode is live AND the last-known (persisted) RemoteConfig.enforcement
 * is true AND the feature's own switch is on; the shadow enforcer otherwise (ARCHITECTURE 1.1, 6).
 */
final class ProxyEnforcement {

    private final BooleanSupplier liveMode;
    private final Supplier<RemoteSettings> remote;
    private final ShadowProxyEnforcer shadow;
    private final ProxyEnforcer live;

    ProxyEnforcement(BooleanSupplier liveMode, Supplier<RemoteSettings> remote, ShadowProxyEnforcer shadow,
                     ProxyEnforcer live) {
        this.liveMode = liveMode;
        this.remote = remote;
        this.shadow = shadow;
        this.live = live;
    }

    boolean liveMode() {
        return liveMode.getAsBoolean();
    }

    boolean cloudEnforcement() {
        return remote.get().enforcement();
    }

    boolean enabled() {
        return liveMode() && cloudEnforcement();
    }

    ProxyEnforcer forFeature(boolean featureEnabled) {
        return enabled() && featureEnabled ? live : shadow;
    }

    /** The UNBAN exception: the unban command for a ban this proxy executed runs in any mode. */
    ProxyEnforcer correctiveUnban() {
        return live;
    }

    ShadowProxyEnforcer shadow() {
        return shadow;
    }

    ProxyEnforcer live() {
        return live;
    }
}
