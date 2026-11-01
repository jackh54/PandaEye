package dev.pandascript.pandaeye.common.litebans;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * The {@code labels.litebans} section of both plugin configs (ARCHITECTURE 4.12): which LiteBans bans become cheat
 * and legit labels for the cloud. Reasons match as case-insensitive substrings; the lists are stored lowercase,
 * blank entries dropped (an entry is not trimmed, so a quoted {@code " alt "} keeps its spaces).
 */
public record LiteBansSettings(boolean enabled, List<String> cheatReasons, List<String> ignoreReasons,
                               List<String> legitUnbanReasons, boolean countConsole, int backfillDays) {

    public static final List<String> DEFAULT_CHEAT_REASONS = List.of("hack", "cheat", "blacklisted mod",
            "modification", "aura", "reach", "fly", "speed", "scaffold", "autoclick", "auto click", "auto totem",
            "autototem", "easy place", "easyplace", "macro", "script", "nuker", "bhop", "velocity", "timer", "client");
    /** Checked before the cheat reasons: x-ray is not something PandaEye detects, bot and alt bans are not cheating. */
    public static final List<String> DEFAULT_IGNORE_REASONS = List.of("x-ray", "xray", "esp", "bot account", "evad",
            "alt", "test");
    public static final List<String> DEFAULT_LEGIT_UNBAN_REASONS = List.of("false ban", "falsely", "wrongful",
            "mistake", "appeal accepted");
    public static final int DEFAULT_BACKFILL_DAYS = 30;
    public static final int MAX_BACKFILL_DAYS = 365;

    public static final LiteBansSettings DEFAULT = new LiteBansSettings(true, DEFAULT_CHEAT_REASONS,
            DEFAULT_IGNORE_REASONS, DEFAULT_LEGIT_UNBAN_REASONS, false, DEFAULT_BACKFILL_DAYS);

    public LiteBansSettings {
        cheatReasons = normalise(cheatReasons);
        ignoreReasons = normalise(ignoreReasons);
        legitUnbanReasons = normalise(legitUnbanReasons);
        backfillDays = Math.max(0, Math.min(MAX_BACKFILL_DAYS, backfillDays));
    }

    private static List<String> normalise(List<String> reasons) {
        List<String> out = new ArrayList<>();
        if (reasons != null) {
            for (String reason : reasons) {
                if (reason != null && !reason.isBlank()) {
                    out.add(reason.toLowerCase(Locale.ROOT));
                }
            }
        }
        return List.copyOf(out);
    }
}
