package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.protocol.v1.PlayerRisk;
import dev.pandascript.pandaeye.protocol.v1.ProbeResult;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import dev.pandascript.pandaeye.protocol.v1.WatchRequest;

/**
 * Callbacks from the {@link CloudClient}. All run on the {@code PandaEye-Cloud} thread: implementations must
 * return quickly and hand any game work to the platform scheduler.
 */
public interface CloudListener {

    default void onStateChanged(CloudState state, String detail) {
    }

    /** New tenant settings (from Welcome, Pong or a config update), or the persisted ones at start. */
    default void onRemoteSettings(RemoteSettings settings) {
    }

    /** Every Verdict must eventually be answered with {@link CloudClient#ackVerdict}. */
    default void onVerdict(Verdict verdict) {
    }

    default void onWatch(WatchRequest watch) {
    }

    default void onPlayerRisk(PlayerRisk risk) {
    }

    default void onProbeResult(ProbeResult result, long rttMicros) {
    }
}
