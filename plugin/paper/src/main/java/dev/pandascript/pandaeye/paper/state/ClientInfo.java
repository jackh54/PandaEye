package dev.pandascript.pandaeye.paper.state;

/**
 * What the plugin knows about the client software.
 *
 * @param protocol     protocol number, -1 when unknown
 * @param tickClient   sends CLIENT_TICK_END (1.21.2+ or newer than PacketEvents knows)
 * @param sendsLoaded  sends PLAYER_LOADED after the loading screen (1.21.4+)
 * @param shiftInput   sneaking is reported only through PLAYER_INPUT (1.21.6+)
 */
public record ClientInfo(int protocol, String version, boolean tickClient, boolean sendsLoaded, boolean shiftInput) {
}
