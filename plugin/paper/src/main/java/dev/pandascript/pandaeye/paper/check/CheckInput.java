package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.SlotTracker;

/**
 * What a check is fed, one input per relevant packet, on the player's netty thread after the
 * {@link PacketState} has been updated. Checks switch over the record types they care about.
 */
public sealed interface CheckInput {

    /** A client flying packet (also rotation-only and status-only packets). */
    record MoveInput(Move move) implements CheckInput {
    }

    /** C CLIENT_TICK_END with the counters of the tick that ended. */
    record TickEndInput(PacketState.TickEnd tick) implements CheckInput {
    }

    /** C INTERACT_ENTITY ({@code attack} for action ATTACK) or the 26.1+ C ATTACK packet. */
    record Interact(int targetEntityId, boolean attack) implements CheckInput {
    }

    /** C PLAYER_BLOCK_PLACEMENT. */
    record Place(int x, int y, int z, int face, float cursorX, float cursorY, float cursorZ, boolean insideBlock)
            implements CheckInput {
    }

    /** C PLAYER_DIGGING; {@code start} is the matching START for FINISH/CANCEL (null if unknown). */
    record Dig(DigAction action, int x, int y, int z, PacketState.DigStart start) implements CheckInput {
    }

    /** C HELD_ITEM_CHANGE, classified against the carriedIndex mirror. */
    record HeldItem(int slot, SlotTracker.Change change) implements CheckInput {
    }

    /** C CLICK_WINDOW; {@code mode} is the click type ordinal. */
    record WindowClick(int windowId, int slot, int button, int mode) implements CheckInput {
    }

    /** C PLAYER_ABILITIES (the client toggled flying). */
    record ClientAbilities(boolean flying) implements CheckInput {
    }

    /** State that invalidates check history: join, dimension change, tick-rate change, hot-load seed. */
    record Reset(ResetReason reason) implements CheckInput {
    }

    enum DigAction {
        START,
        CANCEL,
        FINISH,
        RELEASE_USE_ITEM,
        STAB,
        OTHER
    }

    enum ResetReason {
        JOIN,
        DIMENSION_CHANGE,
        RESPAWN,
        TICK_RATE,
        SEEDED
    }
}
