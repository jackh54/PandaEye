package dev.pandascript.pandaeye.common.transport;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.Locale;
import java.util.regex.Pattern;

/**
 * Decides whether a cloud endpoint may be used: {@code wss://} always, {@code ws://} only to a loopback
 * address unless {@code cloud.allow-insecure} is set (a cloud on the same host or a private network).
 */
public final class EndpointPolicy {

    public static final String INSECURE_WARNING = "cloud endpoint is not encrypted; only use on a private network";

    private static final Pattern IPV4_LOOPBACK = Pattern.compile("127\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}");

    private EndpointPolicy() {
    }

    public enum Kind {
        SECURE,
        INSECURE_LOOPBACK,
        INSECURE_ALLOWED,
        REJECTED
    }

    /** {@code uri} is null when the endpoint was rejected. */
    public record Result(Kind kind, URI uri, String message) {

        public boolean usable() {
            return kind != Kind.REJECTED;
        }
    }

    public static Result evaluate(String endpoint, boolean allowInsecure) {
        URI uri;
        try {
            uri = new URI(endpoint == null ? "" : endpoint.trim());
        } catch (URISyntaxException e) {
            return rejected("cloud endpoint is not a valid URI: " + e.getMessage());
        }
        String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
        String host = uri.getHost();
        if (host == null || host.isEmpty()) {
            return rejected("cloud endpoint has no host: " + endpoint);
        }
        return switch (scheme) {
            case "wss" -> new Result(Kind.SECURE, uri, "");
            case "ws" -> {
                if (isLoopback(host)) {
                    yield new Result(Kind.INSECURE_LOOPBACK, uri, "");
                }
                yield allowInsecure
                        ? new Result(Kind.INSECURE_ALLOWED, uri, INSECURE_WARNING)
                        : rejected("refusing unencrypted cloud endpoint " + endpoint
                                + " (use wss://, a loopback address, or set cloud.allow-insecure: true on a private network)");
            }
            default -> rejected("cloud endpoint must use wss:// (got " + endpoint + ")");
        };
    }

    /** Literal loopback check only: never resolves host names. */
    static boolean isLoopback(String host) {
        String h = host.toLowerCase(Locale.ROOT);
        if (h.startsWith("[") && h.endsWith("]")) {
            h = h.substring(1, h.length() - 1);
        }
        return h.equals("localhost") || h.equals("::1") || h.equals("0:0:0:0:0:0:0:1")
                || IPV4_LOOPBACK.matcher(h).matches();
    }

    private static Result rejected(String message) {
        return new Result(Kind.REJECTED, null, message);
    }
}
