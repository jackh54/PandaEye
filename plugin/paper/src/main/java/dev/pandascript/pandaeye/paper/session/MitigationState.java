package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.protocol.v1.Mitigation;

import java.util.Set;

/**
 * A player's active mitigations from a MITIGATE verdict (ARCHITECTURE 4.8): the set, the strength (0..1) and the
 * verdict that set them. Immutable; lives on the {@link Session}, so it is gone when the player quits.
 */
public record MitigationState(Set<Mitigation> mitigations, double strength, String verdictId) {

    public static final MitigationState NONE = new MitigationState(Set.of(), 0, "");

    public MitigationState {
        mitigations = Set.copyOf(mitigations);
        strength = Double.isFinite(strength) ? Math.max(0, Math.min(1, strength)) : 0;
        verdictId = verdictId == null ? "" : verdictId;
    }

    public boolean active() {
        return !mitigations.isEmpty();
    }

    public boolean has(Mitigation mitigation) {
        return mitigations.contains(mitigation);
    }
}
