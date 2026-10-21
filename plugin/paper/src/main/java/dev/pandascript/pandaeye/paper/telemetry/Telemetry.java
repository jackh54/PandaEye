package dev.pandascript.pandaeye.paper.telemetry;

import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.EventSink;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.CheckStatsWindow;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.EntityIndex;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.EnvSnapshot;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.protocol.v1.CheckStats;
import dev.pandascript.pandaeye.protocol.v1.ContextMove;
import dev.pandascript.pandaeye.protocol.v1.EnvSample;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import dev.pandascript.pandaeye.protocol.v1.LocalFlag;
import dev.pandascript.pandaeye.protocol.v1.MoveSample;
import dev.pandascript.pandaeye.protocol.v1.PlayerJoin;
import dev.pandascript.pandaeye.protocol.v1.PlayerQuit;
import dev.pandascript.pandaeye.protocol.v1.PotionLevel;
import dev.pandascript.pandaeye.protocol.v1.ServerStall;
import dev.pandascript.pandaeye.protocol.v1.StateChange;

import java.util.List;
import java.util.function.Supplier;

/**
 * Builds wire Events and hands them to the transport's {@link EventSink} (ARCHITECTURE 4.6). Thread-safe;
 * per-session focus and down-sampling state lives in {@link SessionTelemetry}. With the cloud disabled the
 * sink is null and nothing is built.
 */
public final class Telemetry {

    private final EventSink sink;
    private final EventClock clock;
    private final Supplier<PluginConfig> config;
    private final EntityIndex entities;

    public Telemetry(EventSink sink, EventClock clock, Supplier<PluginConfig> config, EntityIndex entities) {
        this.sink = sink;
        this.clock = clock;
        this.config = config;
        this.entities = entities;
    }

    public boolean enabled() {
        return sink != null;
    }

    public SessionTelemetry forSession(Session session) {
        return new SessionTelemetry(this, session);
    }

    EventClock clock() {
        return clock;
    }

    PluginConfig config() {
        return config.get();
    }

    EntityIndex entities() {
        return entities;
    }

    /** Bedrock players exempt from checks only produce PlayerJoin, PlayerQuit and StateChange. */
    boolean limited(Session session) {
        return session.bedrock() && config.get().exempt().bedrock();
    }

    Event.Builder event(Session session, long nanos, int tick) {
        return Event.newBuilder()
                .setPlayer(Uuids.toByteString(session.uuid()))
                .setTimeUs(clock.toMicros(nanos))
                .setTick(session.client().tickClient() ? tick : 0);
    }

    void offer(Event event) {
        if (sink != null) {
            sink.offer(event);
        }
    }

    public void playerJoin(Session session, boolean update, long nanos) {
        if (sink == null) {
            return;
        }
        PacketState state = session.state();
        GameMode mode = state.gameMode().latest();
        Boolean bypass = session.signals().bypass();
        PlayerJoin.Builder join = PlayerJoin.newBuilder()
                .setName(session.name())
                .setProtocolVersion(session.client().protocol())
                .setClientVersion(session.client().version())
                .setBrand(state.brand() == null ? "" : state.brand())
                .setBedrock(session.bedrock())
                .setEntityId(state.entityId())
                .setGameMode(mode == null ? GameMode.GAME_MODE_SURVIVAL : mode)
                .setHotLoaded(session.hotLoaded())
                .setExempt(Boolean.TRUE.equals(bypass))
                .setNetworkSession(session.networkSession())
                .setUpdate(update)
                .setSession(session.id().toString());
        offer(event(session, nanos, session.publishedTick()).setJoin(join).build());
    }

    public void playerQuit(Session session, String reason, long nanos) {
        if (sink != null) {
            offer(event(session, nanos, session.publishedTick())
                    .setQuit(PlayerQuit.newBuilder().setReason(reason)).build());
        }
    }

    StateChange stateChange(Session session) {
        PacketState state = session.state();
        AttributeTracker attributes = state.attributes();
        Abilities abilities = state.abilities().latest();
        GameMode mode = state.gameMode().latest();
        StateChange.Builder change = StateChange.newBuilder()
                .setGameMode(mode == null ? GameMode.GAME_MODE_SURVIVAL : mode)
                .setFlying(abilities != null && abilities.flying())
                .setAllowFlight(abilities != null && abilities.allowFlight())
                .setFlySpeed(abilities == null ? 0 : abilities.flySpeed())
                .setMovementSpeed(known(attributes.latest(AttributeTracker.MOVEMENT_SPEED)))
                .setJumpStrength(known(attributes.latest(AttributeTracker.JUMP_STRENGTH)))
                .setGravity(known(attributes.latest(AttributeTracker.GRAVITY)))
                .setStepHeight(known(attributes.latest(AttributeTracker.STEP_HEIGHT)))
                .setScale(known(attributes.latest(AttributeTracker.SCALE)))
                .setEntityReach(known(attributes.latest(AttributeTracker.ENTITY_REACH)))
                .setBlockReach(known(attributes.latest(AttributeTracker.BLOCK_REACH)))
                .setInVehicle(state.inVehicle().latest() == Boolean.TRUE)
                .setHeldSlot(state.slots().mirror())
                .setExempt(Boolean.TRUE.equals(session.signals().bypass()))
                .setBedrock(session.bedrock());
        state.effects().latest().forEach((effect, amplifier) ->
                change.addEffects(PotionLevel.newBuilder().setEffect(effect).setAmplifier(amplifier)));
        return change.build();
    }

    void sendState(Session session, StateChange change, long nanos) {
        offer(event(session, nanos, session.publishedTick()).setState(change).build());
    }

    /** Region thread. */
    public void envSample(Session session, EnvSnapshot env) {
        if (sink == null || limited(session)) {
            return;
        }
        offer(event(session, env.sampledNanos(), session.publishedTick()).setEnv(EnvSample.newBuilder()
                .setFlags(env.flags()).setX(env.x()).setY(env.y()).setZ(env.z())
                .setServerTickMs(env.serverTickMs())).build());
    }

    /** Any thread: a confirmed local flag with its movement context. */
    public void localFlag(Session session, CheckDescriptor check, Violation violation, double vl, List<Move> context,
                          boolean confirmed, long nanos) {
        if (sink == null || limited(session)) {
            return;
        }
        LocalFlag.Builder flag = LocalFlag.newBuilder()
                .setCheckId(check.id())
                .setCategory(check.category())
                .setVl(vl)
                .setSeverity(violation.severity())
                .setConfidence(check.confidence())
                .setInfo(violation.info())
                .putAllData(violation.data())
                .setConfirmed(confirmed)
                .setVariant(violation.variant());
        int tick = session.publishedTick();
        for (Move move : context) {
            flag.addContext(ContextMove.newBuilder().setTimeUs(clock.toMicros(move.nanos())).setTick(move.tick())
                    .setMove(moveSample(move, 1, 1, true)));
            tick = move.tick();
        }
        offer(event(session, nanos, tick).setFlag(flag).build());
    }

    /** Async thread: the 5-minute tuning statistics of one check. */
    public void checkStats(Session session, String checkId, CheckStatsWindow.Snapshot stats, int windowSeconds, long nanos) {
        if (sink == null || limited(session)) {
            return;
        }
        offer(event(session, nanos, session.publishedTick()).setCheckStats(CheckStats.newBuilder()
                .setCheckId(checkId).setMaxStat(stats.maxStat()).setEvaluated(stats.evaluated())
                .setCandidates(stats.candidates()).setDiscarded(stats.discarded()).setConfirmed(stats.confirmed())
                .setWindowS(windowSeconds)).build());
    }

    /** Stall detector thread: a server-level event without a player. */
    public void serverStall(long startNanos, long endNanos, float stalledShare) {
        if (sink != null) {
            offer(Event.newBuilder().setTimeUs(clock.toMicros(startNanos)).setStall(ServerStall.newBuilder()
                    .setStartUs(clock.toMicros(startNanos)).setEndUs(clock.toMicros(endNanos))
                    .setStalledShare(stalledShare)).build());
        }
    }

    static MoveSample moveSample(Move move, int ticksSinceLast, int packetsSinceLast, boolean focus) {
        return MoveSample.newBuilder()
                .setX(move.x()).setY(move.y()).setZ(move.z())
                .setYaw(move.yaw()).setPitch(move.pitch())
                .setOnGround(move.onGround())
                .setHasPos(move.hasPos())
                .setHasRot(move.hasRot())
                .setFlags(move.flags())
                .setTicksSinceLast(ticksSinceLast)
                .setFocus(focus)
                .setHorizontalCollision(move.horizontalCollision())
                .setPacketsSinceLast(packetsSinceLast)
                .build();
    }

    private static double known(double value) {
        return Double.isNaN(value) ? 0 : value;
    }
}
