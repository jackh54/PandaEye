package dev.pandascript.pandaeye.common.litebans;

/**
 * What PandaEye reads of one LiteBans entry, from a live event or a history row. Strings may be null. Only the
 * classifier sees the reasons and the executor; none of them is ever sent.
 *
 * @param player   the punished player's UUID as LiteBans stores it (may be unparsable, e.g. an IP-only ban)
 * @param executor the issuer's UUID string, {@code "CONSOLE"} for the console
 * @param startMs  Unix milliseconds
 * @param endMs    Unix milliseconds, -1 or 0 for a permanent ban
 */
public record BanEntry(long id, String type, String player, String reason, String executor, String removalReason,
                       long startMs, long endMs) {

    public boolean permanent() {
        return endMs <= 0;
    }
}
