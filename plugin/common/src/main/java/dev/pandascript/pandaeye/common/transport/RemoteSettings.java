package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.protocol.v1.RemoteConfig;

/**
 * The last tenant settings the cloud sent, with the time they were received. {@link #UNKNOWN} (enforcement
 * off) applies until a Welcome has ever said otherwise.
 */
public record RemoteSettings(
        int leniency,
        boolean autoban,
        boolean autokick,
        boolean mitigations,
        boolean reputationOnJoin,
        boolean enforcement,
        long receivedUnixMs) {

    public static final RemoteSettings UNKNOWN = new RemoteSettings(4, false, false, false, false, false, 0);

    public static RemoteSettings of(RemoteConfig config, long receivedUnixMs) {
        return new RemoteSettings(config.getLeniency(), config.getAutoban(), config.getAutokick(),
                config.getMitigations(), config.getReputationOnJoin(), config.getEnforcement(), receivedUnixMs);
    }

    public boolean known() {
        return receivedUnixMs > 0;
    }

    boolean sameValues(RemoteSettings other) {
        return leniency == other.leniency && autoban == other.autoban && autokick == other.autokick
                && mitigations == other.mitigations && reputationOnJoin == other.reputationOnJoin
                && enforcement == other.enforcement;
    }
}
