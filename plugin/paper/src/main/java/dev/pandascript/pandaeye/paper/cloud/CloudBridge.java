package dev.pandascript.pandaeye.paper.cloud;

import dev.pandascript.pandaeye.common.transport.CloudListener;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import dev.pandascript.pandaeye.paper.telemetry.WatchRegistry;
import dev.pandascript.pandaeye.protocol.v1.PlayerRisk;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import dev.pandascript.pandaeye.protocol.v1.WatchRequest;

import java.util.Locale;
import java.util.UUID;

/** Routes cloud messages (on the PandaEye-Cloud thread) to the plugin: verdicts, watch requests, risk alerts. */
public final class CloudBridge implements CloudListener {

    private final SessionRegistry sessions;
    private final WatchRegistry watches;
    private final AlertService alerts;
    private final VerdictHandler verdicts;

    public CloudBridge(SessionRegistry sessions, WatchRegistry watches, AlertService alerts, VerdictHandler verdicts) {
        this.sessions = sessions;
        this.watches = watches;
        this.alerts = alerts;
        this.verdicts = verdicts;
    }

    @Override
    public void onVerdict(Verdict verdict) {
        verdicts.handle(verdict);
    }

    @Override
    public void onWatch(WatchRequest watch) {
        UUID player = Uuids.fromByteString(watch.getPlayer()).orElse(null);
        Session session = player == null ? null : sessions.get(player);
        if (session != null) {
            long until = watches.watch(player, watch.getSeconds(), System.nanoTime());
            if (until != Long.MIN_VALUE) {
                session.watchUntil(until);
            }
        }
    }

    @Override
    public void onPlayerRisk(PlayerRisk risk) {
        UUID player = Uuids.fromByteString(risk.getPlayer()).orElse(null);
        Session session = player == null ? null : sessions.get(player);
        if (session != null && risk.getRisk() > 0) {
            alerts.notice("risk|" + player, String.format(Locale.ROOT,
                    "%s has reputation risk %.2f (%d networks flagged, %d confirmed bans)", session.name(),
                    risk.getRisk(), risk.getNetworksFlagged(), risk.getConfirmedBans()), System.nanoTime());
        }
    }
}
