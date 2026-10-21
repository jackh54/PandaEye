package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.protocol.player.ClientVersion;
import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.paper.state.ClientInfo;

/**
 * Client capabilities from the PacketEvents client version. {@code ClientVersion.getById} clamps unknown
 * newer protocols to the newest known version, so they count as tick clients; UNKNOWN reports protocol -1.
 */
public final class ClientInfos {

    private ClientInfos() {
    }

    public static ClientInfo of(User user) {
        ClientVersion version = user.getClientVersion();
        if (version == null || version == ClientVersion.UNKNOWN) {
            return new ClientInfo(-1, "unknown", false, false, false);
        }
        return new ClientInfo(version.getProtocolVersion(), version.getReleaseName(),
                version.isNewerThanOrEquals(ClientVersion.V_1_21_2),
                version.isNewerThanOrEquals(ClientVersion.V_1_21_4),
                version.isNewerThanOrEquals(ClientVersion.V_1_21_6));
    }
}
