package dev.pandascript.pandaeye.common.litebans;

import java.util.EnumSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.UUID;
import java.util.regex.Pattern;

/**
 * Turns a LiteBans ban into a label (ARCHITECTURE 4.12). Only entries of type {@code ban} count. A reason is a
 * cheat reason when one of {@code cheat-reasons} matches it and none of {@code ignore-reasons} does (ignore wins).
 * An entry of any of the three lists matches where it starts a word, case-insensitively: at the start of the reason
 * or after a character that is not a letter or digit, ending anywhere ({@code "hack"} matches "hacking" and
 * {@code "reach"} does not match "breach", so {@code "killaura"} is listed apart from {@code "aura"}); a colour code
 * ({@code &c}, {@code §c}, {@code &#rrggbb}) and a letter of Chinese, Japanese or Thai (written without spaces) count
 * as a separator, and an entry that starts with another character ({@code "#30d"}, {@code "外挂"}) matches anywhere.
 * <ul>
 *   <li>{@link Label#CHEAT}: a ban added with a cheat reason by staff (by the console only with
 *       {@code count-console}).</li>
 *   <li>{@link Label#LEGIT}: a ban with a cheat reason removed with a {@code legit-unban-reasons} reason, whoever
 *       issued it (a wrong console ban lifted by staff is evidence too).</li>
 *   <li>{@link Label#NONE}: everything else (never sent): other reasons and types, unbans without a legit reason
 *       ("ban has been lifted.", unban waves).</li>
 * </ul>
 */
public final class BanClassifier {

    /** Scripts written without spaces between words: a letter of theirs does not continue a word. */
    private static final Set<Character.UnicodeScript> UNSPACED = EnumSet.of(Character.UnicodeScript.HAN,
            Character.UnicodeScript.HIRAGANA, Character.UnicodeScript.KATAKANA, Character.UnicodeScript.THAI,
            Character.UnicodeScript.LAO, Character.UnicodeScript.KHMER, Character.UnicodeScript.MYANMAR);

    public enum Label {
        CHEAT,
        LEGIT,
        NONE
    }

    private final LiteBansSettings settings;

    public BanClassifier(LiteBansSettings settings) {
        this.settings = settings;
    }

    public Label added(BanEntry ban) {
        if (!isBan(ban) || !cheatReason(ban.reason())) {
            return Label.NONE;
        }
        return console(ban.executor()) && !settings.countConsole() ? Label.NONE : Label.CHEAT;
    }

    public Label removed(BanEntry ban) {
        if (!isBan(ban) || !cheatReason(ban.reason())) {
            return Label.NONE;
        }
        return matches(ban.removalReason(), settings.legitUnbanReasons()) ? Label.LEGIT : Label.NONE;
    }

    public boolean cheatReason(String reason) {
        return !matches(reason, settings.ignoreReasons()) && matches(reason, settings.cheatReasons());
    }

    /**
     * The console or anything that is not a player's UUID (LiteBans writes {@code "CONSOLE"}); another anticheat's
     * or PandaEye's own ban-command runs from the console. A Bedrock (Floodgate) staff member's UUID is a player's.
     */
    public static boolean console(String executor) {
        if (executor == null || executor.isBlank()) {
            return true;
        }
        try {
            UUID id = UUID.fromString(executor.trim());
            return id.getMostSignificantBits() == 0 && id.getLeastSignificantBits() == 0;
        } catch (IllegalArgumentException e) {
            return true;
        }
    }

    private static boolean isBan(BanEntry ban) {
        return "ban".equalsIgnoreCase(ban.type());
    }

    /**
     * Whether one of {@code entries} (lowercase, as {@link LiteBansSettings} stores them) starts a word of
     * {@code text}: some occurrence of it, case-insensitively, is at the start of the text, after a character that is
     * not a word character (see {@code wordChar}) or after a colour code ({@code "&cHacking"}). Every occurrence is
     * tried ("reach" matches "breach, reach"); the end is free. An entry that does not start with a word character
     * ({@code "#30d"}, a quoted {@code " alt"}, {@code "外挂"}) brings its own word start and matches anywhere.
     * Hyphens and underscores count as spaces in both ({@code "anti knockback"} matches "Anti-Knockback",
     * {@code "x-ray"} matches "X Ray"), and URLs in the text are skipped: an appeal link
     * ({@code "https://test.example/appeal"}) is not a reason.
     */
    static boolean matches(String text, List<String> entries) {
        if (text == null || text.isEmpty()) {
            return false;
        }
        String lower = fold(URL.matcher(text).replaceAll(" "));
        for (String raw : entries) {
            String entry = fold(raw);
            boolean word = !entry.isEmpty() && wordChar(entry.codePointAt(0));
            for (int at = lower.indexOf(entry); at >= 0; at = lower.indexOf(entry, at + 1)) {
                if (!word || at == 0 || !wordChar(lower.codePointBefore(at)) || colourCodeBefore(lower, at)) {
                    return true;
                }
            }
        }
        return false;
    }

    /** A URL (scheme or "www." up to the next space), as PandaEye's own ban reason carries its appeal link. */
    private static final Pattern URL = Pattern.compile("(?i)(?:\\b[a-z][a-z0-9+.-]*://|\\bwww\\.)\\S*");

    /** Lowercase (Locale.ROOT), with hyphens and underscores as spaces. */
    private static String fold(String text) {
        return text.toLowerCase(Locale.ROOT).replace('-', ' ').replace('_', ' ');
    }

    /**
     * A letter or digit of a script written with spaces between words: Chinese, Japanese and Thai have none
     * ("使用外挂", "using cheats"), so a letter of theirs never continues a word.
     */
    private static boolean wordChar(int codePoint) {
        return Character.isLetterOrDigit(codePoint) && !UNSPACED.contains(Character.UnicodeScript.of(codePoint));
    }

    /**
     * Whether a Minecraft colour or format code ends right before {@code at} in lowercase {@code text}: {@code &c},
     * {@code §c} (also each pair of {@code §x§r§r§g§g§b§b}) or {@code &#rrggbb}, as staff type them into a reason.
     */
    private static boolean colourCodeBefore(String text, int at) {
        if (at >= 2 && (text.charAt(at - 2) == '&' || text.charAt(at - 2) == '§')
                && "0123456789abcdefklmnorx".indexOf(text.charAt(at - 1)) >= 0) {
            return true;
        }
        if (at < 8 || !text.startsWith("&#", at - 8)) {
            return false;
        }
        for (int i = at - 6; i < at; i++) {
            if ("0123456789abcdef".indexOf(text.charAt(i)) < 0) {
                return false;
            }
        }
        return true;
    }
}
