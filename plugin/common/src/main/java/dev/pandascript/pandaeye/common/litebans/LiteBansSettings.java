package dev.pandascript.pandaeye.common.litebans;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * The {@code labels.litebans} section of both plugin configs (ARCHITECTURE 4.12): which LiteBans bans become cheat
 * and legit labels for the cloud. An entry matches where it starts a word of a reason, case-insensitively
 * ({@link BanClassifier}); the lists are stored lowercase, blank entries dropped (an entry is not trimmed, so a quoted
 * {@code "alt "} keeps its space and matches "alt account" but not "alts").
 */
public record LiteBansSettings(boolean enabled, List<String> cheatReasons, List<String> ignoreReasons,
                               List<String> legitUnbanReasons, boolean countConsole, int backfillDays) {

    /**
     * Each cheat in the spellings staff use: an entry only matches at the start of a word, so {@code "aura"} does not
     * match "killaura". {@code "unfair advantage"} is PandaEye's own default ban reason: its console bans are not
     * CHEAT without {@code count-console}, but one lifted with a legit-unban reason is LEGIT.
     */
    public static final List<String> DEFAULT_CHEAT_REASONS = List.of("hack", "cheat", "blacklisted mod",
            "modification", "aura", "reach", "fly", "speed", "scaffold", "autoclick", "auto click", "auto totem",
            "autototem", "easy place", "easyplace", "macro", "script", "nuker", "bhop", "velocity", "timer", "client",
            "killaura", "kill aura", "aimbot", "aim bot", "aim assist", "aimassist", "triggerbot", "trigger bot",
            "antikb", "anti kb", "anti-kb", "antiknockback", "anti knockback", "auto crystal", "autocrystal",
            "auto anchor", "autoanchor", "auto armor", "autoarmor", "nofall", "no fall", "noslow", "no slow",
            "fast place", "fastplace", "hitbox", "criticals", "baritone", "flight", "unfair advantage");
    /** Checked before the cheat reasons: x-ray is not something PandaEye detects, bot and alt bans are not cheating. */
    public static final List<String> DEFAULT_IGNORE_REASONS = List.of("x-ray", "xray", "esp", "bot account", "evad",
            "evasion", "alt", "test");
    public static final List<String> DEFAULT_LEGIT_UNBAN_REASONS = List.of("false ban", "falsely", "wrongful",
            "mistake", "appeal accepted", "false positive", "wrongly", "wrong ban", "accepted appeal", "appeal approved");
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
