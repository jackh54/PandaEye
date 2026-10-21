package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.paper.check.CheckInput;

/**
 * Telemetry-relevant moments reported by the {@link PacketProcessor} after the state is updated (netty
 * thread). The telemetry implementation decides what is sent; tests use {@link #NONE}.
 */
public interface SessionEvents {

    SessionEvents NONE = new SessionEvents() {
    };

    /** JOIN_GAME or a seeded hot-load baseline. */
    default void joined(long nanos) {
    }

    /** A StateChange field (game mode, abilities, attributes, effects, vehicle, held slot) changed. */
    default void stateChanged(long nanos) {
    }

    default void move(long nanos, Move move) {
    }

    /** A position packet that raised no movement violation: the setback target. */
    default void legitPosition(long nanos, Move move) {
    }

    /** {@code targetEntityId} is -1 for a spear stab. */
    default void attack(long nanos, int targetEntityId, boolean stab) {
    }

    default void swing(long nanos, int hand) {
    }

    default void place(long nanos, int x, int y, int z, int face, float cursorX, float cursorY, float cursorZ, int hand,
                       boolean insideBlock) {
    }

    default void dig(long nanos, CheckInput.DigAction action, int x, int y, int z, int face, PacketState.DigStart start) {
    }

    default void click(long nanos, int windowId, int slot, int button, int mode) {
    }

    default void useItem(long nanos, int hand) {
    }

    /** A server velocity or explosion knockback was sent to the player. */
    default void velocity(long nanos, VelocityTracker.Source source, double vx, double vy, double vz) {
    }

    default void latency(long nanos, boolean pong, AckTracker.Reply reply) {
    }

    default void teleport(long nanos, TeleportTracker.Pending teleport, boolean confirmed) {
    }

    /** The first client brand of the session. */
    default void brand(long nanos, String brand) {
    }

    /** A provisional riptide ({@code true}) or elytra exemption was granted and needs validation. */
    default void provisionalExemption(long nanos, boolean riptide) {
    }
}
