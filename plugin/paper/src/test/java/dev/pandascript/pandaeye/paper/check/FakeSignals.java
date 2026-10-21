package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.EnvSnapshot;
import dev.pandascript.pandaeye.paper.state.ExternalSignals;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

/** Test {@link ExternalSignals}: environment flags, exemption and stalls set by the test. */
public final class FakeSignals implements ExternalSignals {

    public static final UUID WORLD = new UUID(7, 7);

    private final List<long[]> stalls = new ArrayList<>();
    private final long[] lastSet = new long[32];
    private int envFlags;
    private boolean exempt;
    private boolean provisionalAllowed = true;
    private EnvSnapshot env = new EnvSnapshot(0, 0, 0, 0, WORLD, 50, 0);

    /** Sets the current environment flags (as if sampled at {@code nanos}). */
    public void env(int flags, long nanos) {
        envFlags = flags;
        for (int bit = 0; bit < 32; bit++) {
            if ((flags & (1 << bit)) != 0) {
                lastSet[bit] = nanos;
            }
        }
        env = new EnvSnapshot(flags, 0, 0, 0, WORLD, 50, nanos);
    }

    public void noWorld() {
        env = null;
    }

    public void exempt(boolean value) {
        exempt = value;
    }

    public void provisionalAllowed(boolean value) {
        provisionalAllowed = value;
    }

    public void stall(long fromNanos, long toNanos) {
        stalls.add(new long[]{fromNanos, toNanos});
    }

    @Override
    public boolean detectionExempt() {
        return exempt;
    }

    @Override
    public EnvSnapshot env() {
        return env;
    }

    @Override
    public boolean envRecent(int envMask, long windowNanos, long nowNanos) {
        if ((envFlags & envMask) != 0) {
            return true;
        }
        for (int bit = 0; bit < 32; bit++) {
            if ((envMask & (1 << bit)) != 0 && lastSet[bit] != 0 && nowNanos - lastSet[bit] <= windowNanos) {
                return true;
            }
        }
        return false;
    }

    @Override
    public boolean provisionalExemptionsAllowed(long nowNanos) {
        return provisionalAllowed;
    }

    @Override
    public boolean stallOverlaps(long fromNanos, long toNanos) {
        for (long[] stall : stalls) {
            if (stall[0] <= toNanos && stall[1] >= fromNanos) {
                return true;
            }
        }
        return false;
    }
}
