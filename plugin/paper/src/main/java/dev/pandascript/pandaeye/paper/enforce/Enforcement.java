package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.common.transport.RemoteSettings;

import java.util.function.BooleanSupplier;
import java.util.function.Supplier;

/**
 * Hands out the live enforcer only when local mode is live, the last-known RemoteConfig.enforcement is true,
 * and the feature's own switch is on; the shadow enforcer otherwise.
 */
public final class Enforcement {

    private final BooleanSupplier liveMode;
    private final Supplier<RemoteSettings> remote;
    private final ShadowEnforcer shadow;
    private final LiveEnforcer live;

    public Enforcement(BooleanSupplier liveMode, Supplier<RemoteSettings> remote, ShadowEnforcer shadow, LiveEnforcer live) {
        this.liveMode = liveMode;
        this.remote = remote;
        this.shadow = shadow;
        this.live = live;
    }

    /** True when enforcement is allowed at all (mode live and the cloud switch on). */
    public boolean enabled() {
        return liveMode.getAsBoolean() && remote.get().enforcement();
    }

    public Enforcer forFeature(boolean featureEnabled) {
        return enabled() && featureEnabled ? live : shadow;
    }

    public ShadowEnforcer shadow() {
        return shadow;
    }

    public LiveEnforcer live() {
        return live;
    }
}
