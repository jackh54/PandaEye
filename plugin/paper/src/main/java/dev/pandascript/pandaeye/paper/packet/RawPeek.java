package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.netty.buffer.ByteBufHelper;

/**
 * Reads a few raw fields of a packet body without a wrapper, always restoring the reader index. Used to skip
 * entity-scoped packets for other entities before decoding them, and for packets whose only field is a
 * VarInt (ANIMATION).
 */
public final class RawPeek {

    private static final int MAX_PASSENGERS = 256;

    private RawPeek() {
    }

    /** The leading VarInt of the packet body (the entity id of entity-scoped client-bound packets). */
    public static int leadingVarInt(Object buffer) {
        int readerIndex = ByteBufHelper.readerIndex(buffer);
        try {
            return ByteBufHelper.readVarInt(buffer);
        } finally {
            ByteBufHelper.readerIndex(buffer, readerIndex);
        }
    }

    /** The leading big-endian int of the packet body (JOIN_GAME's entity id). */
    public static int leadingInt(Object buffer) {
        int readerIndex = ByteBufHelper.readerIndex(buffer);
        try {
            return ByteBufHelper.readInt(buffer);
        } finally {
            ByteBufHelper.readerIndex(buffer, readerIndex);
        }
    }

    /** SET_PASSENGERS: vehicle id and whether {@code entityId} is among the passengers. */
    public record Passengers(int vehicle, boolean includes) {
    }

    public static Passengers passengers(Object buffer, int entityId) {
        int readerIndex = ByteBufHelper.readerIndex(buffer);
        try {
            int vehicle = ByteBufHelper.readVarInt(buffer);
            int count = Math.min(MAX_PASSENGERS, ByteBufHelper.readVarInt(buffer));
            boolean includes = false;
            for (int i = 0; i < count && ByteBufHelper.readableBytes(buffer) > 0; i++) {
                if (ByteBufHelper.readVarInt(buffer) == entityId) {
                    includes = true;
                }
            }
            return new Passengers(vehicle, includes);
        } finally {
            ByteBufHelper.readerIndex(buffer, readerIndex);
        }
    }
}
