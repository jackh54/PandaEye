package litebans.api;

/** Compile-only stub of LiteBans' {@code litebans.api.Entry} (see {@link Events}). */
public abstract class Entry {

    public abstract long getId();

    /** "ban", "mute", "warn" or "kick". */
    public abstract String getType();

    public abstract String getUuid();

    public abstract String getReason();

    /** "CONSOLE" for the console. */
    public abstract String getExecutorUUID();

    public abstract String getExecutorName();

    public abstract String getRemovedByUUID();

    public abstract String getRemovedByName();

    public abstract String getRemovalReason();

    /** Unix milliseconds. */
    public abstract long getDateStart();

    /** Unix milliseconds; -1 or 0 for a permanent entry. */
    public abstract long getDateEnd();

    public abstract String getServerScope();

    public abstract String getServerOrigin();

    public abstract boolean isSilent();

    public abstract boolean isIpban();

    public abstract boolean isActive();
}
