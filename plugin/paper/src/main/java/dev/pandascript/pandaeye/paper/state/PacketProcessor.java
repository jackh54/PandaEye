package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.check.PlayerChecks;
import dev.pandascript.pandaeye.protocol.v1.GameMode;

import java.util.Map;
import java.util.function.LongUnaryOperator;

/**
 * Applies one decoded packet to the {@link PacketState}, then feeds the local checks and reports telemetry
 * moments. The PacketEvents listener and the test harness both drive a session through this class. Netty
 * thread only.
 */
public final class PacketProcessor {

    private final PacketState state;
    private final PlayerChecks checks;
    private final SessionEvents events;
    private final LongUnaryOperator nanosToMicros;

    public PacketProcessor(PacketState state, PlayerChecks checks, SessionEvents events, LongUnaryOperator nanosToMicros) {
        this.state = state;
        this.checks = checks;
        this.events = events;
        this.nanosToMicros = nanosToMicros;
    }

    public PacketState state() {
        return state;
    }

    public PlayerChecks checks() {
        return checks;
    }

    // ---------------------------------------------------------------------------------------------
    // Client -> server
    // ---------------------------------------------------------------------------------------------

    public void flying(double x, double y, double z, float yaw, float pitch, boolean hasPos, boolean hasRot,
                       boolean onGround, boolean horizontalCollision, boolean cancelled, long nanos) {
        Move move = state.flying(x, y, z, yaw, pitch, hasPos, hasRot, onGround, horizontalCollision, cancelled, nanos,
                nanosToMicros.applyAsLong(nanos));
        boolean violation = checks.dispatch(new CheckInput.MoveInput(move), nanos);
        events.move(nanos, move);
        if (!violation && move.hasPos() && !move.teleport()) {
            events.legitPosition(nanos, move);
        }
        state.finishMove(move, cancelled);
    }

    public void tickEnd(long nanos) {
        checks.dispatch(new CheckInput.TickEndInput(state.tickEnd(nanos)), nanos);
    }

    public void teleportConfirm(int id, long nanos) {
        TeleportTracker.Pending confirmed = state.teleportConfirm(id);
        if (confirmed != null) {
            events.teleport(nanos, confirmed, true);
        }
    }

    public void clientKeepAlive(long id, long nanos) {
        events.latency(nanos, false, state.clientKeepAlive(id, nanos));
    }

    public void clientPong(int id, long nanos) {
        events.latency(nanos, true, state.clientPong(id, nanos));
    }

    public void clientHeldItem(int slot, long nanos) {
        checks.dispatch(new CheckInput.HeldItem(slot, state.clientHeldItem(slot)), nanos);
        events.stateChanged(nanos);
    }

    /** INTERACT_ENTITY (any action) or the 26.1+ ATTACK packet. */
    public void interact(int targetEntityId, boolean attack, long nanos) {
        checks.dispatch(new CheckInput.Interact(targetEntityId, attack), nanos);
        if (attack) {
            events.attack(nanos, targetEntityId, false);
        }
    }

    public void animation(int hand, long nanos) {
        events.swing(nanos, hand);
    }

    public void place(int x, int y, int z, int face, float cursorX, float cursorY, float cursorZ, int hand,
                      boolean insideBlock, long nanos) {
        checks.dispatch(new CheckInput.Place(x, y, z, face, cursorX, cursorY, cursorZ, insideBlock), nanos);
        events.place(nanos, x, y, z, face, cursorX, cursorY, cursorZ, hand, insideBlock);
    }

    public void dig(CheckInput.DigAction action, int x, int y, int z, int face, long nanos) {
        PacketState.DigStart start = null;
        switch (action) {
            case START -> state.digStart(x, y, z, nanos);
            case FINISH, CANCEL -> start = state.digEnd(x, y, z);
            case RELEASE_USE_ITEM -> {
                if (state.releaseUseItem(nanos)) {
                    events.provisionalExemption(nanos, true);
                }
            }
            case STAB -> events.attack(nanos, -1, true);
            default -> {
            }
        }
        checks.dispatch(new CheckInput.Dig(action, x, y, z, start), nanos);
        events.dig(nanos, action, x, y, z, face, start);
    }

    public void windowClick(int windowId, int slot, int button, int mode, long nanos) {
        state.windowClick(nanos);
        checks.dispatch(new CheckInput.WindowClick(windowId, slot, button, mode), nanos);
        events.click(nanos, windowId, slot, button, mode);
    }

    public void useItem(int hand, long nanos) {
        state.useItem();
        events.useItem(nanos, hand);
    }

    public void sprinting(boolean sprinting) {
        state.sprinting(sprinting);
    }

    /** ENTITY_ACTION START/STOP_SNEAKING ({@code fromPlayerInput = false}) or PLAYER_INPUT shift. */
    public void sneaking(boolean sneaking, boolean fromPlayerInput) {
        state.sneakingInput(sneaking, fromPlayerInput);
    }

    public void startElytra(long nanos) {
        if (state.startElytra(nanos)) {
            events.provisionalExemption(nanos, false);
        }
    }

    public void vehicleSignal(long nanos) {
        state.vehicleSignal(nanos);
    }

    public void clientAbilities(boolean flying, long nanos) {
        checks.dispatch(new CheckInput.ClientAbilities(flying), nanos);
    }

    public void clientCloseWindow(int windowId, long nanos) {
        state.clientCloseWindow(windowId, nanos);
    }

    public void playerLoaded() {
        state.playerLoaded();
    }

    public void brand(String brand, long nanos) {
        if (state.brand(brand)) {
            events.brand(nanos, brand);
        }
    }

    // ---------------------------------------------------------------------------------------------
    // Server -> client
    // ---------------------------------------------------------------------------------------------

    public void joinGame(int entityId, GameMode mode, String world, long nanos) {
        state.joinGame(entityId, mode, world);
        checks.dispatch(new CheckInput.Reset(CheckInput.ResetReason.JOIN), nanos);
        events.joined(nanos);
    }

    public void respawn(GameMode mode, String world, long nanos) {
        boolean dimensionChanged = state.respawn(mode, world, nanos);
        checks.dispatch(new CheckInput.Reset(dimensionChanged
                ? CheckInput.ResetReason.DIMENSION_CHANGE : CheckInput.ResetReason.RESPAWN), nanos);
        events.stateChanged(nanos);
    }

    public void applyBaseline(Baseline baseline, long nanos) {
        state.applyBaseline(baseline);
        checks.dispatch(new CheckInput.Reset(CheckInput.ResetReason.SEEDED), nanos);
    }

    public void serverTeleport(int id, double x, double y, double z, float yaw, float pitch, boolean relX,
                               boolean relY, boolean relZ, boolean relYaw, boolean relPitch, double deltaX,
                               double deltaY, double deltaZ, long nanos) {
        TeleportTracker.Pending teleport = state.serverTeleport(id, x, y, z, yaw, pitch, relX, relY, relZ, relYaw,
                relPitch, nanos);
        events.teleport(nanos, teleport, false);
        if (deltaX * deltaX + deltaY * deltaY + deltaZ * deltaZ > 1e-6) {
            state.velocity(deltaX, deltaY, deltaZ, nanos, VelocityTracker.Source.PACKET);
        }
    }

    public void serverRotation(float pitch, boolean relativePitch, boolean pitchKnown) {
        state.serverRotation(pitch, relativePitch, pitchKnown);
    }

    public void velocity(double vx, double vy, double vz, VelocityTracker.Source source, long nanos) {
        if (source != VelocityTracker.Source.BUKKIT) {
            events.velocity(nanos, source, vx, vy, vz);
        }
        state.velocity(vx, vy, vz, nanos, source);
    }

    public void serverAbilities(Abilities abilities, long nanos) {
        state.serverAbilities(abilities, nanos);
        events.stateChanged(nanos);
    }

    public void gameMode(GameMode mode, long nanos) {
        state.gameModeChanged(mode, nanos);
        events.stateChanged(nanos);
    }

    public void effect(String key, int amplifier, long nanos) {
        state.effect(key, amplifier, nanos);
        events.stateChanged(nanos);
    }

    public void effectRemoved(String key, long nanos) {
        state.effectRemoved(key, nanos);
        events.stateChanged(nanos);
    }

    /** Attribute values (keys normalised by {@link AttributeTracker#normalise}) without the sprinting modifier. */
    public void attributes(Map<String, Double> values, long nanos) {
        if (values.isEmpty()) {
            return;
        }
        values.forEach((key, value) -> state.attribute(key, value, nanos));
        events.stateChanged(nanos);
    }

    public void metadata(Integer entityFlags, Integer handState, long nanos) {
        state.metadata(entityFlags, handState, nanos);
    }

    public void health(float health) {
        state.health(health);
    }

    public void death() {
        state.death();
    }

    public void tickingState(float rate, boolean frozen, long nanos) {
        state.tickingState(rate, frozen);
        checks.dispatch(new CheckInput.Reset(CheckInput.ResetReason.TICK_RATE), nanos);
    }

    public void passengers(int vehicleId, boolean includesSelf, long nanos) {
        boolean before = state.inVehicle().latest() == Boolean.TRUE;
        state.passengers(vehicleId, includesSelf, nanos);
        if (before != (state.inVehicle().latest() == Boolean.TRUE)) {
            events.stateChanged(nanos);
        }
    }

    public void openWindow(int windowId, String type, long nanos) {
        state.openWindow(windowId, type, nanos);
    }

    public void serverCloseWindow(int windowId, long nanos) {
        state.serverCloseWindow(windowId, nanos);
    }

    public void serverKeepAlive(long id, long nanos) {
        state.serverKeepAlive(id, nanos);
    }

    public void serverPing(int id, long nanos) {
        state.serverPing(id, nanos);
    }

    public void serverHeldItem(int slot) {
        state.serverHeldItem(slot);
    }
}
