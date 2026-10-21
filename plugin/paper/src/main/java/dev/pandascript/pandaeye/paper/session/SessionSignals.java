package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.state.EnvSnapshot;
import dev.pandascript.pandaeye.paper.state.ExternalSignals;

import java.util.concurrent.atomic.AtomicLongArray;
import java.util.function.Supplier;

/**
 * The cross-thread inputs of one session: environment samples (region thread), the bypass permission
 * (entity thread), provisional-exemption validation and server stalls. Volatile and lock-free reads only.
 */
public final class SessionSignals implements ExternalSignals {

    /** Answers whether a server stall overlapped an interval. */
    public interface StallClock {
        boolean overlaps(long fromNanos, long toNanos);
    }

    private final boolean bedrock;
    private final Supplier<PluginConfig> config;
    private final StallClock stalls;
    private final AtomicLongArray envLastSet = new AtomicLongArray(32);
    private final ProvisionalGuard provisional = new ProvisionalGuard();
    private volatile EnvSnapshot env;
    private volatile Boolean bypass;

    public SessionSignals(boolean bedrock, Supplier<PluginConfig> config, StallClock stalls) {
        this.bedrock = bedrock;
        this.config = config;
        this.stalls = stalls;
    }

    /** Region thread: publishes a new environment sample. */
    public void publish(EnvSnapshot snapshot) {
        for (int bit = 0; bit < 32; bit++) {
            if ((snapshot.flags() & (1 << bit)) != 0) {
                envLastSet.set(bit, snapshot.sampledNanos());
            }
        }
        env = snapshot;
    }

    /** Entity thread: the cached result of {@code hasPermission("pandaeye.bypass")}. */
    public void bypass(boolean value) {
        bypass = value;
    }

    /** Null until evaluated. */
    public Boolean bypass() {
        return bypass;
    }

    public boolean bedrock() {
        return bedrock;
    }

    /** Entity thread: a provisional exemption did not match authoritative state. */
    public void provisionalFailed(long nowNanos) {
        provisional.failed(nowNanos);
    }

    @Override
    public boolean detectionExempt() {
        PluginConfig current = config.get();
        if (bedrock && current.exempt().bedrock()) {
            return true;
        }
        Boolean hasBypass = bypass;
        if (current.shadow()) {
            return current.exempt().bypassDetectionInShadow() && Boolean.TRUE.equals(hasBypass);
        }
        return hasBypass == null || hasBypass;
    }

    @Override
    public EnvSnapshot env() {
        return env;
    }

    @Override
    public boolean envRecent(int envMask, long windowNanos, long nowNanos) {
        EnvSnapshot current = env;
        if (current == null) {
            return false;
        }
        if ((current.flags() & envMask) != 0) {
            return true;
        }
        for (int bit = 0; bit < 32; bit++) {
            if ((envMask & (1 << bit)) != 0) {
                long at = envLastSet.get(bit);
                if (at != 0 && nowNanos - at <= windowNanos) {
                    return true;
                }
            }
        }
        return false;
    }

    @Override
    public boolean provisionalExemptionsAllowed(long nowNanos) {
        return provisional.allowed(nowNanos);
    }

    @Override
    public boolean stallOverlaps(long fromNanos, long toNanos) {
        return stalls.overlaps(fromNanos, toNanos);
    }
}
