package dev.pandascript.pandaeye.paper.cloud;

import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.protocol.v1.Action;
import dev.pandascript.pandaeye.protocol.v1.Verdict;

import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.function.Supplier;

/**
 * Receives verdicts (ARCHITECTURE 4.7): de-duplicates by verdict id, logs, alerts staff and acknowledges.
 * This build never executes verdicts: in shadow it acknowledges {@code executed=false} with the shadow reason,
 * otherwise with "not executed: verdict execution not available in this build". A re-delivered verdict gets
 * the same answer again.
 */
public final class VerdictHandler {

    /** Sends a VerdictAck. */
    public interface Acknowledger {
        void ack(String verdictId, boolean executed, String note);
    }

    private static final int REMEMBERED = 10_000;

    private final Supplier<PluginConfig> config;
    private final Supplier<RemoteSettings> remote;
    private final AlertService alerts;
    private final Acknowledger acks;
    private final Map<String, String> answered = new LinkedHashMap<>(256, 0.75f, false) {
        @Override
        protected boolean removeEldestEntry(Map.Entry<String, String> eldest) {
            return size() > REMEMBERED;
        }
    };

    public VerdictHandler(Supplier<PluginConfig> config, Supplier<RemoteSettings> remote, AlertService alerts,
                          Acknowledger acks) {
        this.config = config;
        this.remote = remote;
        this.alerts = alerts;
        this.acks = acks;
    }

    /** Cloud thread. */
    public void handle(Verdict verdict) {
        String id = verdict.getVerdictId();
        String note;
        synchronized (answered) {
            note = answered.get(id);
            if (note == null) {
                note = decide(verdict);
                answered.put(id, note);
                announce(verdict, note);
            }
        }
        acks.ack(id, false, note);
    }

    private String decide(Verdict verdict) {
        if (verdict.getAction() == Action.ACTION_UNBAN) {
            return "not executed here";
        }
        if (config.get().shadow()) {
            return "shadow: local mode";
        }
        if (!remote.get().enforcement()) {
            return "shadow: enforcement off";
        }
        if (verdict.getShadow()) {
            return "shadow: verdict";
        }
        return "not executed: verdict execution not available in this build";
    }

    private void announce(Verdict verdict, String note) {
        String player = verdict.getPlayerName().isEmpty()
                ? Uuids.fromByteString(verdict.getPlayer()).map(Object::toString).orElse("?")
                : verdict.getPlayerName();
        String text = String.format(Locale.ROOT, "verdict %s for %s (score %.1f, confidence %.2f, %s) - %s",
                verdict.getAction().name().replace("ACTION_", ""), player, verdict.getScore(),
                verdict.getConfidence(), String.join(", ", verdict.getChecksList()), note);
        alerts.notice("verdict|" + verdict.getVerdictId(), text, System.nanoTime());
    }
}
