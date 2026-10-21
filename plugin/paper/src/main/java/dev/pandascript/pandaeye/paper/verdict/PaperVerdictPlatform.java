package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.common.verdict.Placeholders;
import dev.pandascript.pandaeye.common.verdict.PunishmentSettings;
import dev.pandascript.pandaeye.common.verdict.VerdictPlatform;
import dev.pandascript.pandaeye.common.verdict.VerdictProcessor;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.enforce.Enforcer;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.log.Json;
import dev.pandascript.pandaeye.paper.log.LogWriter;
import dev.pandascript.pandaeye.paper.session.MitigationState;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import net.kyori.adventure.text.Component;

import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;

/**
 * The Paper/Folia side of verdict handling. Every side effect goes through the {@link Enforcement}'s enforcer:
 * {@code forFeature(...)} hands out the live enforcer only in live mode with cloud enforcement, so a change of
 * mode between the decision and the execution still executes nothing; the UNBAN exception uses
 * {@link Enforcement#correctiveUnban()}. Commands run on the global region thread, kicks on the player's
 * entity thread (both scheduled by the live enforcer).
 */
final class PaperVerdictPlatform implements VerdictPlatform {

    private final Supplier<PluginConfig> config;
    private final Enforcement enforcement;
    private final PlayerLookup players;
    private final MitigationController mitigations;
    private final AlertService alerts;
    private final LogWriter logs;

    PaperVerdictPlatform(Supplier<PluginConfig> config, Enforcement enforcement, PlayerLookup players,
                         MitigationController mitigations, AlertService alerts, LogWriter logs) {
        this.config = config;
        this.enforcement = enforcement;
        this.players = players;
        this.mitigations = mitigations;
        this.alerts = alerts;
        this.logs = logs;
    }

    static PunishmentSettings punishments(PluginConfig.Punishments p) {
        return new PunishmentSettings(p.kickCommand(), p.kickMessage(), p.banCommand(), p.unbanCommand(), p.appealUrl(),
                p.maxBansPerHour(), p.maxKicksPer10Min());
    }

    @Override
    public boolean liveMode() {
        return enforcement.liveMode();
    }

    @Override
    public boolean cloudEnforcement() {
        return enforcement.cloudEnforcement();
    }

    @Override
    public PunishmentSettings punishments() {
        return punishments(config.get().punishments());
    }

    @Override
    public boolean mitigationsSupported() {
        return true;
    }

    @Override
    public boolean mitigationsEnabled() {
        return config.get().mitigations();
    }

    @Override
    public long nowUnixMillis() {
        return System.currentTimeMillis();
    }

    @Override
    public String onlineName(UUID player) {
        PlayerLookup.Online online = players.online(player);
        return online == null ? null : online.name();
    }

    @Override
    public String cachedName(UUID player) {
        return players.cachedName(player);
    }

    @Override
    public boolean exempt(UUID player) {
        PlayerLookup.Online online = players.online(player);
        return online != null && (online.bypass() == null || online.bypass());
    }

    @Override
    public boolean kick(UUID player, String message) {
        PlayerLookup.Online online = players.online(player);
        Enforcer enforcer = enforcement.forFeature(true);
        if (online == null || !enforcer.live()) {
            return false;
        }
        enforcer.kick(online.player(), Component.text(message));
        return true;
    }

    @Override
    public boolean dispatch(String command) {
        Enforcer enforcer = enforcement.forFeature(true);
        if (!enforcer.live() || command.isEmpty()) {
            return false;
        }
        enforcer.dispatchCommand(command);
        return true;
    }

    @Override
    public boolean dispatchUnban(String command) {
        if (command.isEmpty()) {
            return false;
        }
        enforcement.correctiveUnban().dispatchCommand(command);
        return true;
    }

    @Override
    public boolean mitigate(UUID player, Set<Mitigation> set, double strength, String verdictId) {
        PlayerLookup.Online online = players.online(player);
        if (online == null || online.session() == null || !mitigations.allowed()) {
            return false;
        }
        online.session().mitigation(new MitigationState(set, strength, verdictId));
        mitigations.refresh();
        return true;
    }

    @Override
    public boolean unmitigate(UUID player) {
        PlayerLookup.Online online = players.online(player);
        if (online == null || online.session() == null || !online.session().mitigation().active()) {
            return false;
        }
        online.session().mitigation(null);
        return true;
    }

    @Override
    public void alert(String key, String text) {
        alerts.notice(key, text, System.nanoTime());
    }

    @Override
    public void urgent(String text) {
        alerts.urgent(text);
    }

    @Override
    public void logged(VerdictProcessor.Logged entry) {
        if (logs == null || !config.get().alerts().logFile()) {
            return;
        }
        Verdict v = entry.verdict();
        logs.verdict(new Json()
                .put("time", entry.atUnixMs())
                .put("verdict_id", Placeholders.token(v.getVerdictId(), Placeholders.MAX_TOKEN))
                .put("action", v.getAction() == null ? "" : v.getAction().name().replace("ACTION_", ""))
                .put("player", entry.player() == null ? "" : entry.player().toString())
                .put("name", entry.name())
                .put("executed", entry.outcome().executed())
                .put("note", entry.outcome().note())
                .put("duplicate", entry.duplicate())
                .put("shadow", v.getShadow())
                .put("score", v.getScore())
                .put("confidence", v.getConfidence())
                .put("checks", Placeholders.text(String.join(", ", v.getChecksList())))
                .put("reason", Placeholders.text(v.getReason()))
                .put("created", v.getCreatedUnixMs())
                .put("supersedes", Placeholders.token(v.getSupersedes(), Placeholders.MAX_TOKEN)));
    }
}
