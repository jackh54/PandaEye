package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.common.verdict.Placeholders;
import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;
import org.bukkit.command.Command;
import org.bukkit.command.PluginIdentifiableCommand;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.function.Function;

/**
 * The enable-time check of ARCHITECTURE 4.7: the labels of {@code punishments.ban-command} and
 * {@code unban-command} must resolve ({@code Bukkit.getCommandMap().getCommand}) and belong to the same plugin
 * (or both to the server itself), so a ban is never issued by one plugin and lifted by another. Also warns
 * about a kick command that does not resolve and an appeal URL that loses characters when sanitised.
 */
public final class CommandValidator {

    /** The plugin (or "server") that owns a command. */
    static String owner(Command command) {
        if (command instanceof PluginIdentifiableCommand identifiable && identifiable.getPlugin() != null) {
            return identifiable.getPlugin().getName();
        }
        return "server";
    }

    private CommandValidator() {
    }

    /** Returns the warnings; empty when everything is consistent. {@code lookup} is {@code getCommandMap()::getCommand}. */
    public static List<String> validate(PunishmentSettings punishments, Function<String, Command> lookup) {
        List<String> warnings = new ArrayList<>();
        String banLabel = Placeholders.label(punishments.banCommand());
        String unbanLabel = Placeholders.label(punishments.unbanCommand());
        Command ban = banLabel.isEmpty() ? null : lookup.apply(banLabel);
        Command unban = unbanLabel.isEmpty() ? null : lookup.apply(unbanLabel);
        if (banLabel.isEmpty()) {
            warnings.add("punishments.ban-command is empty: BAN verdicts are never executed on this server");
        } else if (ban == null) {
            warnings.add("punishments.ban-command uses '/" + banLabel + "', which is not a registered command:"
                    + " BAN verdicts would fail");
        }
        if (unbanLabel.isEmpty()) {
            if (!banLabel.isEmpty()) {
                warnings.add("punishments.unban-command is empty: overturned bans cannot be lifted automatically");
            }
        } else if (unban == null) {
            warnings.add("punishments.unban-command uses '/" + unbanLabel + "', which is not a registered command:"
                    + " overturned bans would not be lifted");
        }
        if (ban != null && unban != null && !owner(ban).equals(owner(unban))) {
            warnings.add(String.format(Locale.ROOT, "punishments.ban-command ('/%s', from %s) and unban-command"
                            + " ('/%s', from %s) belong to different plugins: an unban would not lift the ban",
                    banLabel, owner(ban), unbanLabel, owner(unban)));
        }
        String kickLabel = Placeholders.label(punishments.kickCommand());
        if (!kickLabel.isEmpty() && lookup.apply(kickLabel) == null) {
            warnings.add("punishments.kick-command uses '/" + kickLabel + "', which is not a registered command");
        }
        if (!Placeholders.cleanUrl(punishments.appealUrl())) {
            warnings.add("punishments.appeal-url contains characters that are removed before it is put into a"
                    + " command (whitespace, quotes, & or %): it becomes '" + Placeholders.url(punishments.appealUrl()) + "'");
        }
        return warnings;
    }
}
