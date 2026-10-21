package dev.pandascript.pandaeye.paper.log;

import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import net.kyori.adventure.text.Component;
import net.kyori.adventure.text.format.NamedTextColor;
import org.bukkit.entity.Player;

import java.util.Locale;
import java.util.UUID;
import java.util.function.Supplier;
import java.util.logging.Logger;

/** Console and in-game staff alerts with the throttling of ARCHITECTURE 4.9. Any thread. */
public final class AlertService {

    private final Logger logger;
    private final Schedulers schedulers;
    private final Supplier<PluginConfig> config;
    private final AlertRecipients recipients = new AlertRecipients();
    private final ConsoleThrottle throttle = new ConsoleThrottle(System.nanoTime());

    public AlertService(Logger logger, Schedulers schedulers, Supplier<PluginConfig> config) {
        this.logger = logger;
        this.schedulers = schedulers;
        this.config = config;
    }

    public AlertRecipients recipients() {
        return recipients;
    }

    /** A confirmed local flag. */
    public void flag(UUID player, String name, String check, double vl, String info, long nanos) {
        int suppressed = throttle.admit(player + "|" + check, nanos);
        if (suppressed < 0) {
            return;
        }
        String text = String.format(Locale.ROOT, "%s failed %s (VL %.1f) %s%s", name, check, vl, info,
                suppressed > 0 ? " [+" + suppressed + " suppressed]" : "");
        send(text);
    }

    /** Cloud messages (verdicts, risk); throttled per key. */
    public void notice(String key, String text, long nanos) {
        int suppressed = throttle.admit(key, nanos);
        if (suppressed >= 0) {
            send(text + (suppressed > 0 ? " [+" + suppressed + " suppressed]" : ""));
        }
    }

    public void forget(UUID player) {
        throttle.forget(player + "|");
    }

    private void send(String text) {
        PluginConfig current = config.get();
        if (current.alerts().console()) {
            logger.info("[alert] " + text);
        }
        Component message = Component.text("[PandaEye] ", NamedTextColor.RED)
                .append(Component.text(text, NamedTextColor.GRAY));
        for (Player staff : recipients.all()) {
            schedulers.runOnEntity(staff, staff.getUniqueId(), task -> staff.sendMessage(message), null, 0);
        }
    }
}
