package dev.pandascript.pandaeye.paper.packet;

import dev.pandascript.pandaeye.protocol.v1.GameMode;

/** PacketEvents game modes to wire game modes; null stays unknown. */
final class GameModes {

    private GameModes() {
    }

    static GameMode of(com.github.retrooper.packetevents.protocol.player.GameMode mode) {
        if (mode == null) {
            return null;
        }
        return switch (mode) {
            case SURVIVAL -> GameMode.GAME_MODE_SURVIVAL;
            case CREATIVE -> GameMode.GAME_MODE_CREATIVE;
            case ADVENTURE -> GameMode.GAME_MODE_ADVENTURE;
            case SPECTATOR -> GameMode.GAME_MODE_SPECTATOR;
        };
    }

    static GameMode ofId(int id) {
        return switch (id) {
            case 0 -> GameMode.GAME_MODE_SURVIVAL;
            case 1 -> GameMode.GAME_MODE_CREATIVE;
            case 2 -> GameMode.GAME_MODE_ADVENTURE;
            case 3 -> GameMode.GAME_MODE_SPECTATOR;
            default -> null;
        };
    }
}
