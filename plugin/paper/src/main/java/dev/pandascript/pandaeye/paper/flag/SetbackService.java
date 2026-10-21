package dev.pandascript.pandaeye.paper.flag;

import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.enforce.Enforcer;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SetbackTarget;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import org.bukkit.entity.Player;

import java.util.function.Supplier;

/**
 * Sets a player back after a confirmed movement or timer violation at or above the check's setback VL, or any
 * while a SETBACK mitigation is active (ARCHITECTURE 4.5). Only the live enforcer moves anyone; the shadow
 * enforcer counts the attempt.
 */
public final class SetbackService {

    private final Enforcement enforcement;
    private final Supplier<PluginConfig> config;

    public SetbackService(Enforcement enforcement, Supplier<PluginConfig> config) {
        this.enforcement = enforcement;
        this.config = config;
    }

    public void consider(Session session, CheckDescriptor check, double vl) {
        if (!check.movement()) {
            return;
        }
        boolean byLevel = check.setbackVl() > 0 && vl >= check.setbackVl();
        boolean mitigated = session.mitigations().contains(Mitigation.MITIGATION_SETBACK);
        Player player = session.player();
        SetbackTarget target = session.setbackTarget();
        if ((!byLevel && !mitigated) || player == null || target == null || target.worldUid() == null) {
            return;
        }
        Enforcer enforcer = enforcement.forFeature(config.get().setbacks());
        enforcer.setback(player, target);
    }
}
