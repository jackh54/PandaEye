package dev.pandascript.pandaeye.paper.telemetry;

import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.AckTracker;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.PositionHistory;
import dev.pandascript.pandaeye.paper.state.SessionEvents;
import dev.pandascript.pandaeye.paper.state.TeleportTracker;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.Attack;
import dev.pandascript.pandaeye.protocol.v1.BlockDig;
import dev.pandascript.pandaeye.protocol.v1.BlockPlace;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import dev.pandascript.pandaeye.protocol.v1.Face;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import dev.pandascript.pandaeye.protocol.v1.Hand;
import dev.pandascript.pandaeye.protocol.v1.InventoryClick;
import dev.pandascript.pandaeye.protocol.v1.LatencySample;
import dev.pandascript.pandaeye.protocol.v1.MoveFlag;
import dev.pandascript.pandaeye.protocol.v1.StateChange;
import dev.pandascript.pandaeye.protocol.v1.Swing;
import dev.pandascript.pandaeye.protocol.v1.TeleportApplied;
import dev.pandascript.pandaeye.protocol.v1.TimedPos;
import dev.pandascript.pandaeye.protocol.v1.UseItem;
import dev.pandascript.pandaeye.protocol.v1.Vec3;
import dev.pandascript.pandaeye.protocol.v1.Vec3i;
import dev.pandascript.pandaeye.protocol.v1.VelocityApplied;

/**
 * Telemetry for one session (netty thread): focus tracking, MoveSample down-sampling, StateChange
 * de-duplication and the per-packet events of ARCHITECTURE 4.6.
 */
public final class SessionTelemetry implements SessionEvents {

    private static final long SECOND = 1_000_000_000L;
    private static final long ACTIVITY_FOCUS_NANOS = 3 * SECOND;
    private static final long FLAG_FOCUS_NANOS = 30 * SECOND;
    private static final long TARGET_HISTORY_MICROS = 1_500_000;
    private static final int MAX_TARGET_HISTORY = 64;

    private final Telemetry telemetry;
    private final Session session;
    private long lastAttack = Long.MIN_VALUE;
    private long lastVelocity = Long.MIN_VALUE;
    private long lastPlaceOrDig = Long.MIN_VALUE;
    private long lastWindowActivity = Long.MIN_VALUE;
    private long lastMoveSent = Long.MIN_VALUE;
    private int tickAtLastSent;
    private int packetsSinceSent;
    private StateChange lastState;

    SessionTelemetry(Telemetry telemetry, Session session) {
        this.telemetry = telemetry;
        this.session = session;
    }

    @Override
    public void joined(long nanos) {
        publish();
        telemetry.playerJoin(session, false, nanos);
        lastState = null;
        stateChanged(nanos);
    }

    @Override
    public void stateChanged(long nanos) {
        publish();
        if (!telemetry.enabled()) {
            return;
        }
        StateChange change = telemetry.stateChange(session);
        if (!change.equals(lastState)) {
            lastState = change;
            telemetry.sendState(session, change, nanos);
        }
    }

    @Override
    public void move(long nanos, Move move) {
        PacketState state = session.state();
        publish();
        packetsSinceSent++;
        if (state.containers().openId() != 0) {
            lastWindowActivity = nanos;
        }
        if (!telemetry.enabled() || telemetry.limited(session)) {
            return;
        }
        boolean focus = inFocus(nanos);
        long idlePeriod = (long) (SECOND / telemetry.config().telemetry().idleSampleHz());
        if (!focus && lastMoveSent != Long.MIN_VALUE && nanos - lastMoveSent < idlePeriod) {
            return;
        }
        int ticks = lastMoveSent == Long.MIN_VALUE ? 0 : Math.max(0, state.tick() - tickAtLastSent);
        telemetry.offer(telemetry.event(session, nanos, state.tick())
                .setMove(Telemetry.moveSample(move, ticks, packetsSinceSent, focus)).build());
        lastMoveSent = nanos;
        tickAtLastSent = state.tick();
        packetsSinceSent = 0;
    }

    @Override
    public void attack(long nanos, int targetEntityId, boolean stab) {
        lastAttack = nanos;
        Session target = stab ? null : telemetry.entities().get(targetEntityId);
        if (target != null && target != session) {
            target.attacked(nanos);
        }
        if (!telemetry.enabled() || telemetry.limited(session)) {
            return;
        }
        PacketState state = session.state();
        double scale = attribute(AttributeTracker.SCALE, 1);
        double eyeHeight = eyeHeight(state) * scale;
        GameMode mode = state.gameMode().latest();
        Attack.Builder attack = Attack.newBuilder()
                .setTargetEntityId(targetEntityId)
                .setEye(vec(state.x(), state.y() + eyeHeight, state.z()))
                .setYaw(state.yaw())
                .setPitch(state.pitch())
                .setSprinting(state.sprinting())
                .setSneaking(state.sneaking())
                .setPingMs((int) state.rtt().pingMillis(nanos))
                .setRttHighMs((int) state.rtt().rttHighMillis(nanos))
                .setEntityReach((float) attribute(AttributeTracker.ENTITY_REACH, 3.0))
                .setGameMode(mode == null ? GameMode.GAME_MODE_SURVIVAL : mode)
                .setEyeHeight((float) eyeHeight)
                .setAttackerInVehicle(state.inVehicleLenient(nanos))
                .setExtendedReach(session.signals().envRecent(EnvFlag.ENV_FLAG_EXTENDED_REACH_ITEM_VALUE, SECOND, nanos))
                .setStab(stab);
        if (target != null) {
            double targetScale = target.publishedScale();
            attack.setTargetPlayer(Uuids.toByteString(target.uuid()))
                    .setTargetType("minecraft:player")
                    .setTargetWidth((float) (0.6 * targetScale))
                    .setTargetHeight((float) (1.8 * targetScale))
                    .setTargetInVehicle(target.publishedInVehicle());
            long since = telemetry.clock().toMicros(nanos) - TARGET_HISTORY_MICROS;
            for (PositionHistory.Entry entry : target.state().positions().since(since, MAX_TARGET_HISTORY)) {
                attack.addTargetHistory(TimedPos.newBuilder().setTimeUs(entry.timeMicros())
                        .setX(entry.x()).setY(entry.y()).setZ(entry.z()));
            }
        }
        telemetry.offer(telemetry.event(session, nanos, state.tick()).setAttack(attack).build());
    }

    @Override
    public void swing(long nanos, int hand) {
        if (enabled()) {
            telemetry.offer(telemetry.event(session, nanos, session.state().tick())
                    .setSwing(Swing.newBuilder().setHand(hand(hand))).build());
        }
    }

    @Override
    public void place(long nanos, int x, int y, int z, int face, float cursorX, float cursorY, float cursorZ, int hand,
                      boolean insideBlock) {
        lastPlaceOrDig = nanos;
        if (!enabled()) {
            return;
        }
        PacketState state = session.state();
        double eye = eyeHeight(state) * attribute(AttributeTracker.SCALE, 1);
        telemetry.offer(telemetry.event(session, nanos, state.tick()).setPlace(BlockPlace.newBuilder()
                .setBlock(Vec3i.newBuilder().setX(x).setY(y).setZ(z))
                .setFace(face(face))
                .setCursorX(cursorX).setCursorY(cursorY).setCursorZ(cursorZ)
                .setHand(hand(hand))
                .setEye(vec(state.x(), state.y() + eye, state.z()))
                .setYaw(state.yaw()).setPitch(state.pitch())
                .setSneaking(state.sneaking())
                .setBlockReach((float) attribute(AttributeTracker.BLOCK_REACH, 4.5))
                .setInsideBlock(insideBlock)).build());
    }

    @Override
    public void dig(long nanos, CheckInput.DigAction action, int x, int y, int z, int face, PacketState.DigStart start) {
        BlockDig.Action wire = switch (action) {
            case START -> BlockDig.Action.ACTION_START;
            case CANCEL -> BlockDig.Action.ACTION_CANCEL;
            case FINISH -> BlockDig.Action.ACTION_FINISH;
            default -> null;
        };
        if (wire == null) {
            return;
        }
        lastPlaceOrDig = nanos;
        if (!enabled()) {
            return;
        }
        PacketState state = session.state();
        double eye = eyeHeight(state) * attribute(AttributeTracker.SCALE, 1);
        int ticks = start == null ? 0 : Math.max(0, state.tick() - start.tick());
        telemetry.offer(telemetry.event(session, nanos, state.tick()).setDig(BlockDig.newBuilder()
                .setAction(wire)
                .setBlock(Vec3i.newBuilder().setX(x).setY(y).setZ(z))
                .setFace(face(face))
                .setEye(vec(state.x(), state.y() + eye, state.z()))
                .setYaw(state.yaw()).setPitch(state.pitch())
                .setTicksSinceStart(ticks)
                .setBlockReach((float) attribute(AttributeTracker.BLOCK_REACH, 4.5))).build());
    }

    @Override
    public void click(long nanos, int windowId, int slot, int button, int mode) {
        lastWindowActivity = nanos;
        if (!enabled()) {
            return;
        }
        PacketState state = session.state();
        boolean open = windowId != 0 && windowId == state.containers().openId();
        telemetry.offer(telemetry.event(session, nanos, state.tick()).setClick(InventoryClick.newBuilder()
                .setWindowId(windowId).setSlot(slot).setButton(button).setMode(mode)
                .setContainerOpen(open)
                .setContainerType(open ? state.containers().openType() : "")).build());
    }

    @Override
    public void useItem(long nanos, int hand) {
        if (enabled()) {
            telemetry.offer(telemetry.event(session, nanos, session.state().tick())
                    .setUseItem(UseItem.newBuilder().setHand(hand(hand))).build());
        }
    }

    @Override
    public void velocity(long nanos, VelocityTracker.Source source, double vx, double vy, double vz) {
        lastVelocity = nanos;
        if (!enabled()) {
            return;
        }
        PacketState state = session.state();
        Move before = state.moves().get(0);
        telemetry.offer(telemetry.event(session, nanos, state.tick()).setVelocity(VelocityApplied.newBuilder()
                .setSource(source == VelocityTracker.Source.EXPLOSION
                        ? VelocityApplied.Source.SOURCE_EXPLOSION : VelocityApplied.Source.SOURCE_VELOCITY_PACKET)
                .setVx(vx).setVy(vy).setVz(vz)
                .setOnGroundBefore(state.onGround())
                .setDyBefore(before == null ? 0 : before.dy())).build());
    }

    @Override
    public void latency(long nanos, boolean pong, AckTracker.Reply reply) {
        if (!enabled() || (reply.matched() && reply.injected())) {
            return;
        }
        telemetry.offer(telemetry.event(session, nanos, session.state().tick()).setLatency(LatencySample.newBuilder()
                .setKind(pong ? LatencySample.Kind.KIND_PONG : LatencySample.Kind.KIND_KEEPALIVE)
                .setRttUs((int) Math.min(Integer.MAX_VALUE, reply.rttNanos() / 1_000))
                .setUnmatched(!reply.matched())).build());
    }

    @Override
    public void teleport(long nanos, TeleportTracker.Pending teleport, boolean confirmed) {
        if (enabled()) {
            telemetry.offer(telemetry.event(session, nanos, session.state().tick())
                    .setTeleport(TeleportApplied.newBuilder().setTeleportId(teleport.id())
                            .setX(teleport.x()).setY(teleport.y()).setZ(teleport.z())
                            .setConfirmed(confirmed)).build());
        }
    }

    @Override
    public void brand(long nanos, String brand) {
        telemetry.playerJoin(session, true, nanos);
    }

    private boolean enabled() {
        return telemetry.enabled() && !telemetry.limited(session);
    }

    private boolean inFocus(long now) {
        PluginConfig config = telemetry.config();
        if (config.telemetry().full() || session.watchUntilNanos() - now > 0) {
            return true;
        }
        long combat = config.telemetry().focusSeconds() * SECOND;
        return within(lastAttack, now, combat) || within(session.lastAttackedNanos(), now, combat)
                || within(lastVelocity, now, ACTIVITY_FOCUS_NANOS) || within(lastPlaceOrDig, now, ACTIVITY_FOCUS_NANOS)
                || within(lastWindowActivity, now, ACTIVITY_FOCUS_NANOS)
                || within(session.lastFlagNanos(), now, FLAG_FOCUS_NANOS);
    }

    private void publish() {
        PacketState state = session.state();
        session.publish(state.tick(), state.inVehicle().latest() == Boolean.TRUE, attribute(AttributeTracker.SCALE, 1),
                state.seeded());
    }

    private double attribute(String key, double fallback) {
        double value = session.state().attributes().latest(key);
        return Double.isNaN(value) ? fallback : value;
    }

    /** Eye height before scale: swimming/gliding 0.4, sneaking 1.27 (1.54 before 1.14), standing 1.62. */
    private double eyeHeight(PacketState state) {
        Move last = state.moves().get(0);
        int flags = last == null ? 0 : last.flags();
        if ((flags & (MoveFlag.MOVE_FLAG_SWIMMING_VALUE | MoveFlag.MOVE_FLAG_GLIDING_VALUE)) != 0) {
            return 0.4;
        }
        if (state.sneaking()) {
            int protocol = session.client().protocol();
            return protocol >= 0 && protocol < 477 ? 1.54 : 1.27;
        }
        return 1.62;
    }

    private static boolean within(long at, long now, long window) {
        return at != Long.MIN_VALUE && now - at < window;
    }

    private static Vec3 vec(double x, double y, double z) {
        return Vec3.newBuilder().setX(x).setY(y).setZ(z).build();
    }

    private static Hand hand(int hand) {
        return hand == 1 ? Hand.HAND_OFF : Hand.HAND_MAIN;
    }

    private static Face face(int face) {
        return face >= 0 && face <= 5 ? Face.forNumber(face) : Face.FACE_OTHER;
    }
}
