package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.check.impl.PitchCheck;
import dev.pandascript.pandaeye.paper.check.impl.SpeedCheck;
import dev.pandascript.pandaeye.paper.check.impl.TimerBalanceCheck;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Function;

/** Which local checks exist and how to create one instance per session. */
public final class CheckRegistry {

    private final Map<String, Function<CheckDescriptor, ? extends Check>> factories = new LinkedHashMap<>();

    /** The checks shipped with the plugin. Add new checks here. */
    public static CheckRegistry defaults() {
        return new CheckRegistry()
                .register(CheckCatalog.TIMER_BALANCE, TimerBalanceCheck::new)
                .register(CheckCatalog.MOVEMENT_SPEED, SpeedCheck::new)
                .register(CheckCatalog.PROTOCOL_PITCH, PitchCheck::new);
    }

    /** Registers a check by its CheckCatalog id; the factory receives the catalog descriptor. */
    public CheckRegistry register(String id, Function<CheckDescriptor, ? extends Check> factory) {
        CheckCatalog.get(id);
        factories.put(id, factory);
        return this;
    }

    public List<String> ids() {
        return List.copyOf(factories.keySet());
    }

    /** Fresh check instances for one session, in registration order. */
    public List<Check> create() {
        List<Check> checks = new ArrayList<>(factories.size());
        factories.forEach((id, factory) -> checks.add(factory.apply(CheckCatalog.get(id))));
        return checks;
    }
}
