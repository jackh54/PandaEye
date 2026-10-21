package dev.pandascript.pandaeye.paper.check;

/**
 * Base class of a local check. One instance exists per player session and is only ever called on that
 * player's netty thread, so instance fields need no synchronisation. A check must never touch Bukkit or the
 * world here; world access belongs in a {@link Confirmer}.
 */
public abstract class Check {

    private final CheckDescriptor descriptor;

    protected Check(CheckDescriptor descriptor) {
        this.descriptor = descriptor;
    }

    public final CheckDescriptor descriptor() {
        return descriptor;
    }

    /** Handles one input; only called while the check is enabled and the session is not exempt. */
    public abstract void handle(CheckInput input, CheckContext context);
}
