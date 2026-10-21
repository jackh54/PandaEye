package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.check.impl.AbilitiesCheck;
import dev.pandascript.pandaeye.paper.check.impl.CursorCheck;
import dev.pandascript.pandaeye.paper.check.impl.DigSpeedCheck;
import dev.pandascript.pandaeye.paper.check.impl.FloodCheck;
import dev.pandascript.pandaeye.paper.check.impl.FlyCheck;
import dev.pandascript.pandaeye.paper.check.impl.GroundSpoofCheck;
import dev.pandascript.pandaeye.paper.check.impl.InventoryClosedCheck;
import dev.pandascript.pandaeye.paper.check.impl.NoSlowCheck;
import dev.pandascript.pandaeye.paper.check.impl.PitchCheck;
import dev.pandascript.pandaeye.paper.check.impl.PositionCheck;
import dev.pandascript.pandaeye.paper.check.impl.SelfHitCheck;
import dev.pandascript.pandaeye.paper.check.impl.SlotCheck;
import dev.pandascript.pandaeye.paper.check.impl.SpeedCheck;
import dev.pandascript.pandaeye.paper.check.impl.StepCheck;
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
                .register(CheckCatalog.MOVEMENT_FLY, FlyCheck::new)
                .register(CheckCatalog.MOVEMENT_STEP, StepCheck::new)
                .register(CheckCatalog.MOVEMENT_GROUNDSPOOF, GroundSpoofCheck::new)
                .register(CheckCatalog.MOVEMENT_NOSLOW, NoSlowCheck::new)
                .register(CheckCatalog.DIG_SPEED, DigSpeedCheck::new)
                .register(CheckCatalog.PROTOCOL_PITCH, PitchCheck::new)
                .register(CheckCatalog.PROTOCOL_POSITION, PositionCheck::new)
                .register(CheckCatalog.PROTOCOL_SLOT, SlotCheck::new)
                .register(CheckCatalog.PROTOCOL_SELFHIT, SelfHitCheck::new)
                .register(CheckCatalog.PROTOCOL_CURSOR, CursorCheck::new)
                .register(CheckCatalog.PROTOCOL_FLOOD, FloodCheck::new)
                .register(CheckCatalog.PROTOCOL_ABILITIES, AbilitiesCheck::new)
                .register(CheckCatalog.INVENTORY_CLOSED, InventoryClosedCheck::new);
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
