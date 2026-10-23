package dev.pandascript.pandaeye.common.proxy;

import dev.pandascript.pandaeye.common.util.Uuids;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Consumer;

/**
 * The {@code pandaeye:session} plugin message a PandaEye proxy sends to a backend after each server connect
 * (73 bytes): version {@code 1} (1) || player uuid (16) || network session uuid (16) || unix seconds
 * (int64, big-endian) || HMAC-SHA256(secret, the preceding 41 bytes) (32). The proxy encodes, the backend
 * verifies.
 */
public final class ProxySessionToken {

    public static final String CHANNEL = "pandaeye:session";
    public static final byte VERSION = 1;
    public static final int LENGTH = 1 + 16 + 16 + 8 + 32;
    public static final long MAX_AGE_SECONDS = 30;

    private static final int SIGNED_LENGTH = 41;

    private ProxySessionToken() {
    }

    public record Verified(UUID player, UUID networkSession, long unixSeconds) {
    }

    /** Why a message was not verified (so a misconfiguration does not fail silently). */
    public enum Rejection {
        /** This server has no secret configured. */
        NO_SECRET,
        /** Wrong length or version. */
        MALFORMED,
        /** The MAC does not match: the proxy uses a different secret (or the message was altered). */
        WRONG_SECRET,
        /** Signed for another player. */
        WRONG_PLAYER,
        /** The timestamp is more than 30 s away: clock skew between the proxy and this server (or a replay). */
        CLOCK_SKEW
    }

    public static byte[] encode(UUID player, UUID networkSession, long unixSeconds, String secret) {
        if (secret == null || secret.isEmpty()) {
            throw new IllegalArgumentException("a proxy session secret is required");
        }
        ByteBuffer buffer = ByteBuffer.allocate(LENGTH);
        buffer.put(VERSION).put(Uuids.toBytes(player)).put(Uuids.toBytes(networkSession)).putLong(unixSeconds);
        buffer.put(mac(secret, buffer.array()));
        return buffer.array();
    }

    /**
     * Verifies a message for {@code expectedPlayer}. Empty when the secret is empty, the version, layout or
     * MAC is wrong, the player differs, or the timestamp is more than 30 s away from {@code nowUnixSeconds}.
     */
    public static Optional<Verified> verify(byte[] message, String secret, UUID expectedPlayer, long nowUnixSeconds) {
        return verify(message, secret, expectedPlayer, nowUnixSeconds, rejection -> { });
    }

    /** As {@link #verify(byte[], String, UUID, long)}; an empty result is reported to {@code rejected} first. */
    public static Optional<Verified> verify(byte[] message, String secret, UUID expectedPlayer, long nowUnixSeconds,
                                            Consumer<Rejection> rejected) {
        if (secret == null || secret.isEmpty()) {
            rejected.accept(Rejection.NO_SECRET);
            return Optional.empty();
        }
        if (message == null || message.length != LENGTH || message[0] != VERSION) {
            rejected.accept(Rejection.MALFORMED);
            return Optional.empty();
        }
        byte[] expected = mac(secret, message);
        byte[] actual = new byte[32];
        System.arraycopy(message, SIGNED_LENGTH, actual, 0, 32);
        if (!MessageDigest.isEqual(expected, actual)) {
            rejected.accept(Rejection.WRONG_SECRET);
            return Optional.empty();
        }
        UUID player = Uuids.fromBytes(message, 1);
        UUID session = Uuids.fromBytes(message, 17);
        long unixSeconds = ByteBuffer.wrap(message, 33, 8).getLong();
        if (!player.equals(expectedPlayer)) {
            rejected.accept(Rejection.WRONG_PLAYER);
            return Optional.empty();
        }
        if (Math.abs(nowUnixSeconds - unixSeconds) > MAX_AGE_SECONDS) {
            rejected.accept(Rejection.CLOCK_SKEW);
            return Optional.empty();
        }
        return Optional.of(new Verified(player, session, unixSeconds));
    }

    private static byte[] mac(String secret, byte[] message) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
            mac.update(message, 0, SIGNED_LENGTH);
            return mac.doFinal();
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("HmacSHA256 unavailable", e);
        }
    }
}
