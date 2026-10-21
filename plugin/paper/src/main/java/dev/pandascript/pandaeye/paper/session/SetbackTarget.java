package dev.pandascript.pandaeye.paper.session;

import java.util.UUID;

/** The last position that raised no movement violation; where a live setback sends the player. */
public record SetbackTarget(double x, double y, double z, float yaw, float pitch, UUID worldUid) {
}
