package dev.pandascript.pandaeye.paper.state;

import java.util.UUID;

/**
 * One environment sample taken on the player's region thread (ARCHITECTURE 4.4). Published through a
 * volatile field; immutable.
 *
 * @param flags        {@code EnvFlag} bits
 * @param serverTickMs measured duration of the player's region tick (50 at 20 TPS)
 */
public record EnvSnapshot(int flags, double x, double y, double z, UUID worldUid, float serverTickMs, long sampledNanos) {

    public boolean has(int envFlag) {
        return (flags & envFlag) != 0;
    }
}
