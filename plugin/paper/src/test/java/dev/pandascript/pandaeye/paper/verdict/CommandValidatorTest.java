package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;
import dev.pandascript.pandaeye.paper.TestProxies;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.command.PluginIdentifiableCommand;
import org.bukkit.plugin.Plugin;
import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The enable-time check of the ban and unban command labels (ARCHITECTURE 4.7). */
class CommandValidatorTest {

    private final Map<String, Command> commands = new HashMap<>();

    private static Command vanilla(String name) {
        return new Command(name) {
            @Override
            public boolean execute(CommandSender sender, String label, String[] args) {
                return true;
            }
        };
    }

    private static final class PluginCommandStub extends Command implements PluginIdentifiableCommand {
        private final Plugin plugin;

        PluginCommandStub(String name, String pluginName) {
            super(name);
            this.plugin = TestProxies.of(Plugin.class, Map.of("getName", pluginName));
        }

        @Override
        public boolean execute(CommandSender sender, String label, String[] args) {
            return true;
        }

        @Override
        public Plugin getPlugin() {
            return plugin;
        }
    }

    private static PunishmentSettings settings(String ban, String unban, String kick, String appealUrl) {
        return new PunishmentSettings(kick, "msg", ban, unban, appealUrl, 3, 5);
    }

    private List<String> validate(PunishmentSettings settings) {
        return CommandValidator.validate(settings, commands::get);
    }

    @Test
    void theDefaultVanillaCommandsAreConsistent() {
        commands.put("ban", vanilla("ban"));
        commands.put("pardon", vanilla("pardon"));
        assertEquals(List.of(), validate(settings("ban %player% Unfair advantage. Appeal at %appeal_url% with code %appeal_code%",
                "pardon %player%", "", "")));
    }

    @Test
    void sameBanPluginIsConsistentAndNamespacedLabelsResolve() {
        commands.put("litebans:ban", new PluginCommandStub("ban", "LiteBans"));
        commands.put("unban", new PluginCommandStub("unban", "LiteBans"));
        assertEquals(List.of(), validate(settings("/LiteBans:ban %player% %reason%", "unban %player%", "", "")));
    }

    @Test
    void missingCommandsAreReported() {
        commands.put("pardon", vanilla("pardon"));
        List<String> warnings = validate(settings("tempban %player% %duration%", "pardon %player%", "kickall", ""));
        assertEquals(2, warnings.size(), warnings.toString());
        assertTrue(warnings.get(0).contains("'/tempban'"), warnings.get(0));
        assertTrue(warnings.get(1).contains("kick-command"), warnings.get(1));
    }

    @Test
    void banAndUnbanFromDifferentPluginsAreReported() {
        commands.put("ban", new PluginCommandStub("ban", "LiteBans"));
        commands.put("pardon", vanilla("pardon"));
        List<String> warnings = validate(settings("ban %player%", "pardon %player%", "", ""));
        assertEquals(1, warnings.size());
        assertTrue(warnings.get(0).contains("different plugins"), warnings.get(0));
        assertTrue(warnings.get(0).contains("LiteBans") && warnings.get(0).contains("server"), warnings.get(0));
    }

    @Test
    void emptyCommandsAndBadAppealUrlsAreReported() {
        List<String> warnings = validate(settings("", "", "", "https://appeal.example/?a=1&b=2"));
        assertEquals(2, warnings.size(), warnings.toString());
        assertTrue(warnings.get(0).contains("ban-command is empty"));
        assertTrue(warnings.get(1).contains("appeal-url"));
        commands.put("ban", vanilla("ban"));
        assertTrue(validate(settings("ban %player%", "", "", "")).get(0).contains("unban-command is empty"));
    }
}
