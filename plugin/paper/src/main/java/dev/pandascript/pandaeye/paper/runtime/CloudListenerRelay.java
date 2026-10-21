package dev.pandascript.pandaeye.paper.runtime;

import dev.pandascript.pandaeye.common.transport.CloudListener;
import dev.pandascript.pandaeye.common.transport.CloudState;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.protocol.v1.PlayerRisk;
import dev.pandascript.pandaeye.protocol.v1.ProbeResult;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import dev.pandascript.pandaeye.protocol.v1.WatchRequest;

/**
 * Lets the transport be created before the plugin parts that consume its callbacks (which themselves need the
 * transport): callbacks go to the target set before the transport is started.
 */
final class CloudListenerRelay implements CloudListener {

    private volatile CloudListener target = new CloudListener() {
    };

    void target(CloudListener listener) {
        target = listener;
    }

    @Override
    public void onStateChanged(CloudState state, String detail) {
        target.onStateChanged(state, detail);
    }

    @Override
    public void onRemoteSettings(RemoteSettings settings) {
        target.onRemoteSettings(settings);
    }

    @Override
    public void onVerdict(Verdict verdict) {
        target.onVerdict(verdict);
    }

    @Override
    public void onWatch(WatchRequest watch) {
        target.onWatch(watch);
    }

    @Override
    public void onPlayerRisk(PlayerRisk risk) {
        target.onPlayerRisk(risk);
    }

    @Override
    public void onProbeResult(ProbeResult result, long rttMicros) {
        target.onProbeResult(result, rttMicros);
    }
}
