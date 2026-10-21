package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.command.CommandMeta;
import dev.pandascript.pandaeye.common.verdict.Placeholders;
import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.function.Function;

/**
 * The proxy's version of the ban/unban command check (ARCHITECTURE 4.7, 6): configured commands must be
 * registered proxy commands, and the ban and unban commands must belong to the same plugin. Empty commands are
 * the default on the proxy (bans are handled by the backends) and are not reported.
 */
final class ProxyCommandCheck {

    private ProxyCommandCheck() {
    }

    static List<String> validate(PunishmentSettings punishments, Function<String, CommandMeta> lookup) {
        List<String> warnings = new ArrayList<>();
        String banLabel = Placeholders.label(punishments.banCommand());
        String unbanLabel = Placeholders.label(punishments.unbanCommand());
        String kickLabel = Placeholders.label(punishments.kickCommand());
        CommandMeta ban = banLabel.isEmpty() ? null : lookup.apply(banLabel);
        CommandMeta unban = unbanLabel.isEmpty() ? null : lookup.apply(unbanLabel);
        if (!banLabel.isEmpty() && ban == null) {
            warnings.add("punishments.ban-command uses '/" + banLabel + "', which is not a registered proxy command");
        }
        if (!unbanLabel.isEmpty() && unban == null) {
            warnings.add("punishments.unban-command uses '/" + unbanLabel + "', which is not a registered proxy command");
        }
        if (!banLabel.isEmpty() && unbanLabel.isEmpty()) {
            warnings.add("punishments.unban-command is empty: overturned bans cannot be lifted automatically");
        }
        if (ban != null && unban != null && !Objects.equals(ban.getPlugin(), unban.getPlugin())) {
            warnings.add("punishments.ban-command ('/" + banLabel + "') and unban-command ('/" + unbanLabel
                    + "') belong to different plugins: an unban would not lift the ban");
        }
        if (!kickLabel.isEmpty() && lookup.apply(kickLabel) == null) {
            warnings.add("punishments.kick-command uses '/" + kickLabel + "', which is not a registered proxy command");
        }
        if (!Placeholders.cleanUrl(punishments.appealUrl())) {
            warnings.add("punishments.appeal-url contains characters that are removed before it is put into a command"
                    + " (whitespace, quotes, & or %): it becomes '" + Placeholders.url(punishments.appealUrl()) + "'");
        }
        return warnings;
    }
}
