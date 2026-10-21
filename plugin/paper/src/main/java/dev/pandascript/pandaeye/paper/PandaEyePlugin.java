package dev.pandascript.pandaeye.paper;

import dev.pandascript.pandaeye.paper.runtime.Runtime;
import org.bukkit.plugin.java.JavaPlugin;

import java.util.logging.Level;

/** Plugin entry point; all work happens in {@link Runtime}. */
public final class PandaEyePlugin extends JavaPlugin {

    private Runtime runtime;

    @Override
    public void onEnable() {
        Runtime candidate = new Runtime(this);
        try {
            candidate.enable();
            runtime = candidate;
        } catch (Throwable t) {
            getLogger().log(Level.SEVERE, "PandaEye failed to enable and stays inactive", t);
            candidate.disable();
            getServer().getPluginManager().disablePlugin(this);
        }
    }

    @Override
    public void onDisable() {
        Runtime current = runtime;
        runtime = null;
        if (current != null) {
            current.disable();
        }
    }
}
