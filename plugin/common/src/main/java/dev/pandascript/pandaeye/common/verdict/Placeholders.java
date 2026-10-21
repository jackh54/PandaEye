package dev.pandascript.pandaeye.common.verdict;

import java.util.List;
import java.util.Locale;
import java.util.UUID;
import java.util.regex.Pattern;

/**
 * Placeholder expansion for punishment commands and messages (ARCHITECTURE 4.7). Every value is sanitised
 * before it is inserted and the template is expanded in a single pass, so a value can never introduce another
 * placeholder, a line break, a colour code or a command separator:
 *
 * <ul>
 *   <li>{@code %player%}: the name resolved locally from the UUID; it must match {@link #PLAYER_NAME} or the
 *       verdict is not executed at all ({@link #validName});</li>
 *   <li>{@code %uuid%}: the player UUID;</li>
 *   <li>{@code %reason%}, {@code %checks%}: restricted to {@code [A-Za-z0-9 ._,()x-]}, whitespace collapsed,
 *       at most 128 characters;</li>
 *   <li>{@code %verdict_id%}, {@code %appeal_code%}: {@code [A-Za-z0-9-]} only;</li>
 *   <li>{@code %appeal_url%}: from the local config, URL characters only (no {@code &}, {@code %}, quotes or
 *       whitespace);</li>
 *   <li>{@code %duration%}: {@code ban_duration_s} as {@code 7d}/{@code 12h}/{@code 30m}/{@code 45s}, empty for
 *       0 (permanent).</li>
 * </ul>
 *
 * The finished line has control characters and the section sign removed, runs of whitespace collapsed and a
 * leading {@code /} dropped. Unknown {@code %...%} sequences are left as they are.
 */
public final class Placeholders {

    public static final Pattern PLAYER_NAME = Pattern.compile("^[A-Za-z0-9_.]{1,16}$");
    public static final int MAX_TEXT = 128;
    public static final int MAX_TOKEN = 64;
    public static final int MAX_URL = 256;
    public static final int MAX_LINE = 1024;

    private static final List<String> KEYS = List.of("player", "uuid", "reason", "checks", "verdict_id",
            "appeal_code", "appeal_url", "duration");

    private Placeholders() {
    }

    /** The values of one expansion; all are sanitised again on insertion. */
    public record Values(String player, UUID uuid, String reason, String checks, String verdictId,
                         String appealCode, String appealUrl, long durationSeconds) {
    }

    /** True for a Minecraft name PandaEye may put into a command. */
    public static boolean validName(String name) {
        return name != null && PLAYER_NAME.matcher(name).matches();
    }

    /** {@code [A-Za-z0-9 ._,()x-]} only, whitespace collapsed and trimmed, at most 128 characters. */
    public static String text(String raw) {
        if (raw == null) {
            return "";
        }
        StringBuilder out = new StringBuilder(Math.min(raw.length(), MAX_TEXT));
        boolean space = false;
        for (int i = 0; i < raw.length() && out.length() < MAX_TEXT; i++) {
            char c = raw.charAt(i);
            if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
                space = !out.isEmpty();
                continue;
            }
            if (!textChar(c)) {
                continue;
            }
            if (space) {
                out.append(' ');
                space = false;
                if (out.length() >= MAX_TEXT) {
                    break;
                }
            }
            out.append(c);
        }
        return out.toString().trim();
    }

    /** {@code [A-Za-z0-9-]} only, at most {@code max} characters. */
    public static String token(String raw, int max) {
        if (raw == null) {
            return "";
        }
        StringBuilder out = new StringBuilder();
        for (int i = 0; i < raw.length() && out.length() < max; i++) {
            char c = raw.charAt(i);
            if (alnum(c) || c == '-') {
                out.append(c);
            }
        }
        return out.toString();
    }

    /** URL characters without whitespace, quotes, {@code &}, {@code %}, {@code <>}, backslash; at most 256. */
    public static String url(String raw) {
        if (raw == null) {
            return "";
        }
        StringBuilder out = new StringBuilder();
        for (int i = 0; i < raw.length() && out.length() < MAX_URL; i++) {
            char c = raw.charAt(i);
            if (alnum(c) || "._~:/?#@!$()*+,;=-".indexOf(c) >= 0) {
                out.append(c);
            }
        }
        return out.toString();
    }

    /** True when {@link #url} keeps the value unchanged (used to warn about a bad {@code appeal-url}). */
    public static boolean cleanUrl(String raw) {
        return raw == null || raw.equals(url(raw));
    }

    /** {@code 7d}, {@code 12h}, {@code 90m} or {@code 45s} (largest exact unit); empty for 0 or less. */
    public static String duration(long seconds) {
        if (seconds <= 0) {
            return "";
        }
        if (seconds % 86_400 == 0) {
            return seconds / 86_400 + "d";
        }
        if (seconds % 3_600 == 0) {
            return seconds / 3_600 + "h";
        }
        if (seconds % 60 == 0) {
            return seconds / 60 + "m";
        }
        return seconds + "s";
    }

    /** The command label of a template: its first word, without a leading slash, lowercase. */
    public static String label(String template) {
        String line = finish(template == null ? "" : template);
        int space = line.indexOf(' ');
        return (space < 0 ? line : line.substring(0, space)).toLowerCase(Locale.ROOT);
    }

    /** Expands {@code template} with sanitised values in one pass, then {@link #finish}es the line. */
    public static String expand(String template, Values values) {
        if (template == null || template.isEmpty()) {
            return "";
        }
        StringBuilder out = new StringBuilder(template.length() + 64);
        int i = 0;
        while (i < template.length()) {
            char c = template.charAt(i);
            if (c == '%') {
                int end = template.indexOf('%', i + 1);
                if (end > i + 1) {
                    String key = template.substring(i + 1, end);
                    if (KEYS.contains(key)) {
                        out.append(value(key, values));
                        i = end + 1;
                        continue;
                    }
                }
            }
            out.append(c);
            i++;
        }
        return finish(out.toString());
    }

    /**
     * Removes control characters (line breaks included), the section sign and other invisible format
     * characters, collapses whitespace, trims, drops leading slashes and caps the length.
     */
    public static String finish(String line) {
        StringBuilder out = new StringBuilder(Math.min(line.length(), MAX_LINE));
        boolean space = false;
        for (int i = 0; i < line.length() && out.length() < MAX_LINE; i++) {
            char c = line.charAt(i);
            if (Character.isWhitespace(c) || Character.isSpaceChar(c)) {
                space = !out.isEmpty();
                continue;
            }
            if (Character.isISOControl(c) || c == '§' || Character.getType(c) == Character.FORMAT
                    || Character.getType(c) == Character.SURROGATE && !validSurrogate(line, i)) {
                continue;
            }
            if (out.isEmpty() && c == '/') {
                continue;
            }
            if (space) {
                out.append(' ');
                space = false;
            }
            out.append(c);
        }
        return out.toString();
    }

    private static String value(String key, Values values) {
        return switch (key) {
            case "player" -> validName(values.player()) ? values.player() : "";
            case "uuid" -> values.uuid() == null ? "" : values.uuid().toString();
            case "reason" -> text(values.reason());
            case "checks" -> text(values.checks());
            case "verdict_id" -> token(values.verdictId(), MAX_TOKEN);
            case "appeal_code" -> token(values.appealCode(), MAX_TOKEN);
            case "appeal_url" -> url(values.appealUrl());
            case "duration" -> duration(values.durationSeconds());
            default -> "";
        };
    }

    private static boolean textChar(char c) {
        return alnum(c) || c == ' ' || c == '.' || c == '_' || c == ',' || c == '(' || c == ')' || c == '-';
    }

    private static boolean alnum(char c) {
        return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9');
    }

    private static boolean validSurrogate(String s, int i) {
        char c = s.charAt(i);
        if (Character.isHighSurrogate(c)) {
            return i + 1 < s.length() && Character.isLowSurrogate(s.charAt(i + 1));
        }
        return i > 0 && Character.isHighSurrogate(s.charAt(i - 1));
    }
}
