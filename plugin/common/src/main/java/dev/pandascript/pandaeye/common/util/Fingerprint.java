package dev.pandascript.pandaeye.common.util;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.HexFormat;

/** Short, non-reversible identifiers for secrets that must never be logged in full. */
public final class Fingerprint {

    private Fingerprint() {
    }

    /** {@code sha256:} plus the first 8 hex digits of the SHA-256 of the value, or {@code <empty>}. */
    public static String of(String secret) {
        if (secret == null || secret.isEmpty()) {
            return "<empty>";
        }
        try {
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(secret.getBytes(StandardCharsets.UTF_8));
            return "sha256:" + HexFormat.of().formatHex(digest, 0, 4);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 unavailable", e);
        }
    }
}
