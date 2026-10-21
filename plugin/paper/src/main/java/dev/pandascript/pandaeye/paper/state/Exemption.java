package dev.pandascript.pandaeye.paper.state;

/** Why local movement checks are skipped for a packet (CHECKS 1.1 "common exemptions"), first match wins. */
public enum Exemption {
    /** Bedrock, or the bypass permission outside shadow mode. */
    EXEMPT_PLAYER,
    /** Older clients get no local movement checks in v1. */
    NOT_TICK_CLIENT,
    /** Hot-loaded and the snapshot has not been applied yet. */
    UNSEEDED,
    /** Fewer than 40 movement packets since join or seed. */
    WARMING_UP,
    /** A required state (abilities, game mode, attributes, effects, position) is unknown. */
    STATE_UNKNOWN,
    GAME_MODE,
    DEAD,
    VEHICLE,
    /** Flying (applied or pending) or flying ended less than 1 s ago. */
    FLYING,
    GLIDING,
    RIPTIDE,
    LEVITATION,
    TELEPORT,
    VELOCITY,
    /** Loading screen after JOIN_GAME/RESPAWN. */
    LOADING,
    /** Server tick rate not 20 or the game is frozen. */
    TICK_RATE,
    /** This and the previous packet were cancelled by another plugin. */
    CANCELLED,
    /** Water, lava, bubble column, climbing, web, powder snow, honey, stuck or an entity nearby in the last second. */
    ENVIRONMENT
}
