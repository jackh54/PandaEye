package dev.pandascript.pandaeye.common.util;

import com.google.protobuf.ByteString;

import java.nio.ByteBuffer;
import java.util.Optional;
import java.util.UUID;

/** UUID <-> 16-byte big-endian conversions used on the wire. */
public final class Uuids {

    public static final int LENGTH = 16;

    private Uuids() {
    }

    public static byte[] toBytes(UUID uuid) {
        return ByteBuffer.allocate(LENGTH)
                .putLong(uuid.getMostSignificantBits())
                .putLong(uuid.getLeastSignificantBits())
                .array();
    }

    public static ByteString toByteString(UUID uuid) {
        return ByteString.copyFrom(toBytes(uuid));
    }

    public static UUID fromBytes(byte[] bytes, int offset) {
        ByteBuffer buffer = ByteBuffer.wrap(bytes, offset, LENGTH);
        return new UUID(buffer.getLong(), buffer.getLong());
    }

    /** Empty when the byte string is not exactly 16 bytes long. */
    public static Optional<UUID> fromByteString(ByteString bytes) {
        if (bytes.size() != LENGTH) {
            return Optional.empty();
        }
        return Optional.of(fromBytes(bytes.toByteArray(), 0));
    }

    /** True for a canonical lowercase UUID string ({@code 8-4-4-4-12} hex digits). */
    public static boolean isCanonical(String value) {
        if (value == null || value.length() != 36) {
            return false;
        }
        for (int i = 0; i < 36; i++) {
            char c = value.charAt(i);
            boolean dash = i == 8 || i == 13 || i == 18 || i == 23;
            if (dash ? c != '-' : !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) {
                return false;
            }
        }
        return true;
    }
}
