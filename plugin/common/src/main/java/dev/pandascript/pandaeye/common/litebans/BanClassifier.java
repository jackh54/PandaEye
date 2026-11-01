package dev.pandascript.pandaeye.common.litebans;

import java.util.List;
import java.util.Locale;
import java.util.UUID;

/**
 * Turns a LiteBans ban into a label (ARCHITECTURE 4.12). Only entries of type {@code ban} count. A reason is a
 * cheat reason when one of {@code cheat-reasons} matches it and none of {@code ignore-reasons} does (ignore wins).
 * An entry of any of the three lists matches where it starts a word, case-insensitively: at the start of the reason
 * or after a character that is not a letter or digit, ending anywhere ({@code "hack"} matches "hacking" and
 * {@code "reach"} does not match "breach", so {@code "killaura"} is listed apart from {@code "aura"}); an entry
 * that starts with another character ({@code "#30d"}) matches anywhere.
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
     * {@code text}: some occurrence of it, case-insensitively, is at the start of the text or after a character that
     * is not a letter or digit. Every occurrence is tried ("reach" matches "breach, reach"); the end is free. An entry
     * that does not start with a letter or digit ({@code "#30d"}, a quoted {@code " alt"}) brings its own word start
     * and matches anywhere.
     */
    static boolean matches(String text, List<String> entries) {
        if (text == null || text.isEmpty()) {
            return false;
        }
        String lower = text.toLowerCase(Locale.ROOT);
        for (String entry : entries) {
            boolean word = !entry.isEmpty() && Character.isLetterOrDigit(entry.codePointAt(0));
            for (int at = lower.indexOf(entry); at >= 0; at = lower.indexOf(entry, at + 1)) {
                if (!word || at == 0 || !Character.isLetterOrDigit(lower.codePointBefore(at))) {
                    return true;
                }
            }
        }
        return false;
    }
}
