package dev.pandascript.pandaeye.common.verdict;

/**
 * The {@code punishments} section of a plugin config (ARCHITECTURE 4.11). Templates may use the placeholders of
 * {@link Placeholders}; an empty {@code kickCommand} kicks with {@code kickMessage}, an empty ban or unban
 * command disables that action.
 */
public record PunishmentSettings(String kickCommand, String kickMessage, String banCommand, String unbanCommand,
                                 String appealUrl, int maxBansPerHour, int maxKicksPer10Min) {

    public PunishmentSettings {
        kickCommand = kickCommand == null ? "" : kickCommand.trim();
        kickMessage = kickMessage == null ? "" : kickMessage;
        banCommand = banCommand == null ? "" : banCommand.trim();
        unbanCommand = unbanCommand == null ? "" : unbanCommand.trim();
        appealUrl = appealUrl == null ? "" : appealUrl.trim();
    }
}
