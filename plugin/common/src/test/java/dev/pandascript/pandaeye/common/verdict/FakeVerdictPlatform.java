package dev.pandascript.pandaeye.common.verdict;

import dev.pandascript.pandaeye.protocol.v1.Mitigation;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

/** A scriptable {@link VerdictPlatform} that records every side effect. */
final class FakeVerdictPlatform implements VerdictPlatform {

    boolean live = true;
    boolean enforcement = true;
    boolean mitigationsSupported = true;
    boolean mitigationsEnabled = true;
    boolean refuseSideEffects;
    long now = 1_700_000_000_000L;
    PunishmentSettings punishments = new PunishmentSettings("", "Unfair advantage detected (%reason%)",
            "ban %player% Unfair advantage. Appeal at %appeal_url% with code %appeal_code%", "pardon %player%",
            "https://appeal.example/a", 3, 5);
    final Map<UUID, String> online = new HashMap<>();
    final Map<UUID, String> cache = new HashMap<>();
    final Set<UUID> exempt = new HashSet<>();
    final Map<UUID, Set<Mitigation>> mitigations = new HashMap<>();
    final Map<UUID, Double> strengths = new HashMap<>();
    final List<String> commands = new ArrayList<>();
    final List<String> unbanCommands = new ArrayList<>();
    final List<String> kicks = new ArrayList<>();
    final List<String> alerts = new ArrayList<>();
    final List<String> urgent = new ArrayList<>();
    final List<VerdictProcessor.Logged> logged = new ArrayList<>();

    @Override
    public boolean liveMode() {
        return live;
    }

    @Override
    public boolean cloudEnforcement() {
        return enforcement;
    }

    @Override
    public PunishmentSettings punishments() {
        return punishments;
    }

    @Override
    public boolean mitigationsSupported() {
        return mitigationsSupported;
    }

    @Override
    public boolean mitigationsEnabled() {
        return mitigationsEnabled;
    }

    @Override
    public long nowUnixMillis() {
        return now;
    }

    @Override
    public String onlineName(UUID player) {
        return online.get(player);
    }

    @Override
    public String cachedName(UUID player) {
        return cache.get(player);
    }

    @Override
    public boolean exempt(UUID player) {
        return exempt.contains(player);
    }

    @Override
    public boolean kick(UUID player, String message) {
        if (refuseSideEffects || !online.containsKey(player)) {
            return false;
        }
        kicks.add(online.get(player) + ": " + message);
        return true;
    }

    @Override
    public boolean dispatch(String command) {
        if (refuseSideEffects) {
            return false;
        }
        commands.add(command);
        return true;
    }

    @Override
    public boolean dispatchUnban(String command) {
        unbanCommands.add(command);
        return true;
    }

    @Override
    public boolean mitigate(UUID player, Set<Mitigation> set, double strength, String verdictId) {
        if (refuseSideEffects || !online.containsKey(player)) {
            return false;
        }
        mitigations.put(player, Set.copyOf(set));
        strengths.put(player, strength);
        return true;
    }

    @Override
    public boolean unmitigate(UUID player) {
        strengths.remove(player);
        return mitigations.remove(player) != null;
    }

    @Override
    public void alert(String key, String text) {
        alerts.add(text);
    }

    @Override
    public void urgent(String text) {
        urgent.add(text);
    }

    @Override
    public void logged(VerdictProcessor.Logged entry) {
        logged.add(entry);
    }

    int sideEffects() {
        return commands.size() + kicks.size() + mitigations.size() + unbanCommands.size();
    }
}
