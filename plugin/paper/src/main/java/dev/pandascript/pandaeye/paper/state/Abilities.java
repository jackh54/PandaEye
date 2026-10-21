package dev.pandascript.pandaeye.paper.state;

/** Server PLAYER_ABILITIES as the client sees them. */
public record Abilities(boolean flying, boolean allowFlight, boolean creative, float flySpeed) {
}
