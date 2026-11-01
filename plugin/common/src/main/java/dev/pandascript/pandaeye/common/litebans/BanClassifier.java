package dev.pandascript.pandaeye.common.litebans;

import java.util.List;
import java.util.Locale;
import java.util.UUID;

/**
 * Turns a LiteBans ban into a label (ARCHITECTURE 4.12). Only entries of type {@code ban} count. A reason is a
 * cheat reason when it contains one of {@code cheat-reasons} and none of {@code ignore-reasons} (ignore wins),
 * case-insensitively.
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
        return contains(ban.removalReason(), settings.legitUnbanReasons()) ? Label.LEGIT : Label.NONE;
    }

    public boolean cheatReason(String reason) {
        return !contains(reason, settings.ignoreReasons()) && contains(reason, settings.cheatReasons());
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

    private static boolean contains(String text, List<String> needles) {
        if (text == null || text.isEmpty()) {
            return false;
        }
        String lower = text.toLowerCase(Locale.ROOT);
        for (String needle : needles) {
            if (lower.contains(needle)) {
                return true;
            }
        }
        return false;
    }
}
