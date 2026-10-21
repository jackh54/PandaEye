package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.List;
import java.util.function.BiConsumer;

/** The local checks of one session, fed on its netty thread. */
public final class PlayerChecks {

    private final PacketState state;
    private final List<CheckHandle> handles;
    private final BiConsumer<String, Throwable> errors;

    public PlayerChecks(PacketState state, List<CheckHandle> handles, BiConsumer<String, Throwable> errors) {
        this.state = state;
        this.handles = List.copyOf(handles);
        this.errors = errors;
    }

    public List<CheckHandle> handles() {
        return handles;
    }

    /**
     * Feeds every enabled check. Nothing runs before the session is seeded or while the player is exempt from
     * detection. Returns true when a movement or timer check raised a flag or candidate for this input.
     */
    public boolean dispatch(CheckInput input, long nanos) {
        if (!state.seeded() || state.signals().detectionExempt()) {
            return false;
        }
        boolean movementViolation = false;
        for (CheckHandle handle : handles) {
            try {
                if (handle.handle(input, nanos) && handle.descriptor().movement()) {
                    movementViolation = true;
                }
            } catch (Throwable t) {
                errors.accept("check " + handle.descriptor().id(), t);
            }
        }
        return movementViolation;
    }
}
