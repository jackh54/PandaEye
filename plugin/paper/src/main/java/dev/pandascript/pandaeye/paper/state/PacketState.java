package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import dev.pandascript.pandaeye.protocol.v1.MoveFlag;

/**
 * Everything PandaEye tracks about one player at packet level (ARCHITECTURE 4.3, CHECKS 1.1). Owned by the
 * player's netty thread: the mutators are package-private and called by {@link PacketProcessor}; the public
 * accessors are the read-only view that local checks use. Unknown state is represented explicitly (NaN,
 * null, -1, {@code known() == false}) and must make checks skip, never assume a default.
 */
public final class PacketState {

    /** Fallback after which a server change counts as applied without an acknowledgement. */
    public static final long APPLY_FALLBACK_NANOS = 5_000_000_000L;
    /** Movement packets needed after join/seed before local movement checks run. */
    public static final int WARMUP_MOVES = 40;

    static final int ENTITY_FLAG_SNEAKING = 0x02;
    static final int ENTITY_FLAG_SPRINTING = 0x08;
    static final int ENTITY_FLAG_SWIMMING = 0x10;
    static final int ENTITY_FLAG_GLIDING = 0x80;
    static final int HAND_ACTIVE = 0x01;
    static final int HAND_RIPTIDE = 0x04;

    private static final long SECOND = 1_000_000_000L;
    private static final long FLYING_GRACE_NANOS = SECOND;
    private static final long ENV_WINDOW_NANOS = SECOND;
    private static final long VEHICLE_SIGNAL_GRACE_NANOS = SECOND;
    private static final long ELYTRA_PROVISIONAL_NANOS = 2 * SECOND;
    private static final int ENV_EXEMPT_MASK = EnvFlag.ENV_FLAG_IN_WATER_VALUE | EnvFlag.ENV_FLAG_IN_LAVA_VALUE
            | EnvFlag.ENV_FLAG_IN_BUBBLE_COLUMN_VALUE | EnvFlag.ENV_FLAG_CLIMBING_VALUE | EnvFlag.ENV_FLAG_IN_WEB_VALUE
            | EnvFlag.ENV_FLAG_IN_POWDER_SNOW_VALUE | EnvFlag.ENV_FLAG_ON_HONEY_VALUE | EnvFlag.ENV_FLAG_STUCK_VALUE
            | EnvFlag.ENV_FLAG_NEAR_ENTITY_VALUE;

    /** Promotion clock for one movement packet: applied when acknowledged before the packet, or after 5 s. */
    private final class MoveClock implements AckClock {
        long boundary;
        boolean valid;
        long now;

        @Override
        public boolean applied(long sentNanos) {
            return (valid && boundary - sentNanos >= 0) || now - sentNanos >= APPLY_FALLBACK_NANOS;
        }
    }

    /** Applied when acknowledged at all (any time up to now), or after 5 s. */
    private final AckClock currentClock = this::appliedByClient;
    private final MoveClock moveClock = new MoveClock();

    private final ClientInfo client;
    private final ExternalSignals signals;
    private final AckTracker acks = new AckTracker();
    private final RttTracker rtt = new RttTracker();
    private final TeleportTracker teleports = new TeleportTracker();
    private final VelocityTracker velocities = new VelocityTracker();
    private final ContainerTracker containers = new ContainerTracker();
    private final SlotTracker slots = new SlotTracker();
    private final AttributeTracker attributes = new AttributeTracker();
    private final EffectTracker effects = new EffectTracker();
    private final MoveHistory moves = new MoveHistory();
    private final PositionHistory positions = new PositionHistory();
    private final Tracked<GameMode> gameMode = new Tracked<>(null);
    private final Tracked<Abilities> abilities = new Tracked<>(null);
    private final Tracked<Boolean> inVehicle = new Tracked<>(null);
    private final Tracked<Integer> entityFlags = new Tracked<>(null);
    private final Tracked<Integer> handState = new Tracked<>(null);

    private boolean seeded;
    private int entityId = -1;
    private int vehicleId = -1;
    private int movesSinceSeed;
    private int epoch;
    private String worldName = "";

    private boolean positionKnown;
    private boolean rotationKnown;
    private double x;
    private double y;
    private double z;
    private float yaw;
    private float pitch;
    private boolean onGround;
    private boolean previousCancelled;

    private int tickEnds;
    private int tickAtLastMove = Integer.MIN_VALUE;
    private int flyingThisTick;
    private int teleportConfirmsThisTick;
    private int serverRotationsThisTick;

    private boolean dead;
    private float tickRate = 20;
    private boolean frozen;
    private boolean loading;
    private int positionsSinceSpawn;
    private boolean serverRotationNext;
    private boolean serverPitchOutOfRange;
    private long lastFlyingNanos;
    private boolean everFlying;
    private long lastVehicleSignalNanos;
    private boolean anyVehicleSignal;
    private boolean clientSprinting;
    private boolean clientSneaking;
    private long provisionalRiptideUntil;
    private long provisionalGlideUntil;
    private boolean provisionalRiptide;
    private boolean provisionalGlide;
    private int useItemTick = Integer.MIN_VALUE;
    private boolean releasedSinceUse = true;
    private DigStart digStart;
    private String brand;

    /** A PLAYER_DIGGING START that has not been finished or cancelled. */
    public record DigStart(int x, int y, int z, int tick, long nanos) {
    }

    /** Counters of the tick that just ended (protocol.flood). */
    public record TickEnd(int tick, int flyingPackets, int teleportConfirms, int serverRotations, long nanos) {
    }

    public PacketState(ClientInfo client, ExternalSignals signals) {
        this.client = client;
        this.signals = signals;
    }

    // ---------------------------------------------------------------------------------------------
    // Lifecycle
    // ---------------------------------------------------------------------------------------------

    /** S JOIN_GAME (a real join, or re-entry to PLAY after configuration): everything starts over. */
    void joinGame(int entityId, GameMode mode, String world) {
        this.entityId = entityId;
        this.worldName = world == null ? "" : world;
        seeded = true;
        gameMode.reset(mode);
        abilities.reset(null);
        inVehicle.reset(Boolean.FALSE);
        vehicleId = -1;
        entityFlags.reset(0);
        handState.reset(0);
        attributes.resetToDefaults();
        effects.resetEmpty();
        slots.reset(0);
        containers.unknown();
        tickEnds = 0;
        resetMovement();
    }

    /** S RESPAWN. Returns true when the dimension (world) changed. */
    boolean respawn(GameMode mode, String world, long nanos) {
        boolean changed = world == null || !world.equals(worldName);
        worldName = world == null ? "" : world;
        if (mode != null) {
            gameMode.sent(mode, nanos);
        }
        dead = false;
        inVehicle.sent(Boolean.FALSE, nanos);
        vehicleId = -1;
        containers.unknown();
        resetMovement();
        return changed;
    }

    /** Applies a hot-load snapshot; from now on the session is seeded. */
    void applyBaseline(Baseline baseline) {
        entityId = baseline.entityId();
        seeded = true;
        gameMode.reset(baseline.gameMode());
        abilities.reset(baseline.abilities());
        inVehicle.reset(baseline.inVehicle());
        vehicleId = baseline.inVehicle() ? baseline.vehicleId() : -1;
        entityFlags.reset(baseline.gliding() ? ENTITY_FLAG_GLIDING : 0);
        handState.reset(baseline.riptiding() ? HAND_RIPTIDE : 0);
        attributes.reset(baseline.attributes());
        effects.reset(baseline.effects());
        dead = baseline.dead();
        slots.reset(baseline.heldSlot());
        movesSinceSeed = 0;
        if (brand == null && baseline.brand() != null && !baseline.brand().isEmpty()) {
            brand = baseline.brand();
        }
    }

    private void resetMovement() {
        positionKnown = false;
        rotationKnown = false;
        onGround = false;
        loading = true;
        positionsSinceSpawn = 0;
        movesSinceSeed = 0;
        tickAtLastMove = Integer.MIN_VALUE;
        teleports.clear();
        velocities.clear();
        moves.clear();
        digStart = null;
        epoch++;
    }

    // ---------------------------------------------------------------------------------------------
    // Client movement
    // ---------------------------------------------------------------------------------------------

    /** A client flying packet; returns the carried-forward movement. Call {@link #finishMove} after checks. */
    Move flying(double rawX, double rawY, double rawZ, float rawYaw, float rawPitch, boolean hasPos, boolean hasRot,
                boolean packetOnGround, boolean horizontalCollision, boolean cancelled, long nanos, long eventMicros) {
        moveClock.valid = acks.hasAck();
        moveClock.boundary = acks.ackedUpTo();
        moveClock.now = nanos;
        boolean teleport = false;
        if (hasPos) {
            TeleportTracker.Pending confirmed = teleports.takeConfirmed();
            teleport = confirmed != null;
        }
        double nx = hasPos ? rawX : x;
        double ny = hasPos ? rawY : y;
        double nz = hasPos ? rawZ : z;
        boolean deltas = hasPos && positionKnown && !teleport;
        double dx = deltas ? nx - x : 0;
        double dy = deltas ? ny - y : 0;
        double dz = deltas ? nz - z : 0;
        boolean consecutive = client.tickClient() && tickAtLastMove != Integer.MIN_VALUE
                && tickEnds == tickAtLastMove + 1 && flyingThisTick == 0;
        boolean ground = teleport ? onGround : packetOnGround;
        int flags = moveFlags(hasRot, cancelled) | (teleport && hasRot ? MoveFlag.MOVE_FLAG_SERVER_ROTATION_VALUE : 0);

        x = nx;
        y = ny;
        z = nz;
        if (hasRot) {
            yaw = rawYaw;
            pitch = rawPitch;
            rotationKnown = true;
            serverRotationNext = false;
        }
        onGround = ground;
        positionKnown |= hasPos;
        tickAtLastMove = tickEnds;
        flyingThisTick++;
        if (hasPos) {
            positionsSinceSpawn++;
            positions.add(eventMicros, nx, ny, nz);
            if (!client.sendsLoaded() && positionsSinceSpawn >= TeleportTracker.GRACE_POSITIONS) {
                loading = false;
            }
        }
        if (flyingLenient()) {
            lastFlyingNanos = nanos;
            everFlying = true;
        }
        Move move = new Move(nanos, tickEnds, nx, ny, nz, yaw, pitch, ground, hasPos, hasRot, horizontalCollision,
                dx, dy, dz, consecutive, teleport, cancelled, flags, rawX, rawY, rawZ, rawYaw, rawPitch);
        moves.add(move);
        movesSinceSeed++;
        return move;
    }

    /** Ends a movement packet: velocities and pending state the client has applied by now are promoted. */
    void finishMove(Move move, boolean wasCancelled) {
        if (move.hasPos()) {
            teleports.onPositionPacket();
        }
        velocities.onMove(move.hasPos(), move.dy(), moveClock, moveClock.now);
        gameMode.promote(moveClock);
        abilities.promote(moveClock);
        inVehicle.promote(moveClock);
        entityFlags.promote(moveClock);
        handState.promote(moveClock);
        attributes.promote(moveClock);
        effects.promote(moveClock);
        previousCancelled = wasCancelled;
        if (provisionalGlide && moveClock.now - provisionalGlideUntil >= 0) {
            provisionalGlide = false;
        }
        if (provisionalRiptide && moveClock.now - provisionalRiptideUntil >= 0) {
            provisionalRiptide = false;
        }
    }

    /** C CLIENT_TICK_END: returns the counters of the tick that ended. */
    TickEnd tickEnd(long nanos) {
        TickEnd ended = new TickEnd(tickEnds, flyingThisTick, teleportConfirmsThisTick, serverRotationsThisTick, nanos);
        tickEnds++;
        flyingThisTick = 0;
        teleportConfirmsThisTick = 0;
        serverRotationsThisTick = 0;
        return ended;
    }

    private int moveFlags(boolean hasRot, boolean cancelled) {
        int flags = 0;
        if (clientSprinting) {
            flags |= MoveFlag.MOVE_FLAG_SPRINTING_VALUE;
        }
        if (sneaking()) {
            flags |= MoveFlag.MOVE_FLAG_SNEAKING_VALUE;
        }
        if (entityFlags.anyMatch(f -> (f & ENTITY_FLAG_SWIMMING) != 0)) {
            flags |= MoveFlag.MOVE_FLAG_SWIMMING_VALUE;
        }
        if (glidingLenient()) {
            flags |= MoveFlag.MOVE_FLAG_GLIDING_VALUE;
        }
        if (inVehicleLenient()) {
            flags |= MoveFlag.MOVE_FLAG_IN_VEHICLE_VALUE;
        }
        if (flyingLenient()) {
            flags |= MoveFlag.MOVE_FLAG_FLYING_VALUE;
        }
        if (riptideLenient()) {
            flags |= MoveFlag.MOVE_FLAG_RIPTIDE_VALUE;
        }
        if (handState.anyMatch(h -> (h & HAND_ACTIVE) != 0)) {
            flags |= MoveFlag.MOVE_FLAG_USING_ITEM_VALUE;
        }
        if (teleports.pending()) {
            flags |= MoveFlag.MOVE_FLAG_TELEPORT_PENDING_VALUE;
        }
        if (velocities.pending()) {
            flags |= MoveFlag.MOVE_FLAG_VELOCITY_PENDING_VALUE;
        }
        if (containers.openId() != 0) {
            flags |= MoveFlag.MOVE_FLAG_CONTAINER_OPEN_VALUE;
        }
        if (dead) {
            flags |= MoveFlag.MOVE_FLAG_DEAD_VALUE;
        }
        if (hasRot && serverRotationNext) {
            flags |= MoveFlag.MOVE_FLAG_SERVER_ROTATION_VALUE;
        }
        if (cancelled) {
            flags |= MoveFlag.MOVE_FLAG_CANCELLED_VALUE;
        }
        return flags;
    }

    // ---------------------------------------------------------------------------------------------
    // Server packets
    // ---------------------------------------------------------------------------------------------

    /** S PLAYER_POSITION_AND_LOOK with relative parts resolved against the last known position. */
    TeleportTracker.Pending serverTeleport(int id, double tx, double ty, double tz, float tyaw, float tpitch,
                                           boolean relX, boolean relY, boolean relZ, boolean relYaw,
                                           boolean relPitch, long nanos) {
        double ax = relX ? x + tx : tx;
        double ay = relY ? y + ty : ty;
        double az = relZ ? z + tz : tz;
        float ayaw = relYaw ? yaw + tyaw : tyaw;
        float apitch = relPitch ? pitch + tpitch : tpitch;
        serverPitchOutOfRange = Math.abs(apitch) > 90;
        TeleportTracker.Pending pending = new TeleportTracker.Pending(id, ax, ay, az, ayaw, apitch, nanos);
        teleports.sent(pending);
        return pending;
    }

    /** S PLAYER_ROTATION / FACE_PLAYER: the next rotation-bearing packet is a server rotation. */
    void serverRotation(float targetPitch, boolean relativePitch, boolean pitchKnown) {
        serverRotationNext = true;
        serverRotationsThisTick++;
        if (pitchKnown) {
            float absolute = relativePitch ? pitch + targetPitch : targetPitch;
            serverPitchOutOfRange = Math.abs(absolute) > 90;
        } else {
            serverPitchOutOfRange = false;
        }
    }

    void velocity(double vx, double vy, double vz, long nanos, VelocityTracker.Source source) {
        velocities.add(vx, vy, vz, nanos, source);
    }

    void serverAbilities(Abilities value, long nanos) {
        abilities.sent(value, nanos);
    }

    void gameModeChanged(GameMode mode, long nanos) {
        gameMode.sent(mode, nanos);
    }

    void effect(String key, int amplifier, long nanos) {
        effects.sent(key, amplifier, nanos);
    }

    void effectRemoved(String key, long nanos) {
        effects.sent(key, -1, nanos);
    }

    void attribute(String key, double valueWithoutSprint, long nanos) {
        attributes.sent(key, valueWithoutSprint, nanos);
    }

    /** Own ENTITY_METADATA: entity flags byte (index 0) and living hand-state byte; null when not in the packet. */
    void metadata(Integer flags, Integer hands, long nanos) {
        if (flags != null) {
            entityFlags.sent(flags, nanos);
        }
        if (hands != null) {
            handState.sent(hands, nanos);
        }
    }

    void health(float health) {
        if (health <= 0) {
            dead = true;
        }
    }

    void death() {
        dead = true;
    }

    /** S TICKING_STATE; returns true when the tick rate is not normal (timer resets). */
    boolean tickingState(float rate, boolean isFrozen) {
        tickRate = rate;
        frozen = isFrozen;
        return !tickRateNormal();
    }

    /** S SET_PASSENGERS for a vehicle; {@code includesSelf} = the player is one of its passengers. */
    void passengers(int vehicle, boolean includesSelf, long nanos) {
        if (includesSelf) {
            vehicleId = vehicle;
            inVehicle.sent(Boolean.TRUE, nanos);
        } else if (vehicle == vehicleId) {
            vehicleId = -1;
            inVehicle.sent(Boolean.FALSE, nanos);
        }
    }

    void openWindow(int id, String type, long nanos) {
        containers.open(id, type, nanos);
    }

    void serverCloseWindow(int id, long nanos) {
        containers.serverClose(id, nanos);
    }

    void serverKeepAlive(long id, long nanos) {
        acks.serverKeepAlive(id, nanos);
    }

    void serverPing(int id, long nanos) {
        acks.serverPing(id, nanos);
    }

    void serverHeldItem(int slot) {
        slots.serverSet(slot);
    }

    // ---------------------------------------------------------------------------------------------
    // Client packets other than movement
    // ---------------------------------------------------------------------------------------------

    TeleportTracker.Pending teleportConfirm(int id) {
        TeleportTracker.Pending pending = teleports.confirm(id);
        if (pending != null) {
            teleportConfirmsThisTick++;
        }
        return pending;
    }

    AckTracker.Reply clientKeepAlive(long id, long nanos) {
        AckTracker.Reply reply = acks.clientKeepAlive(id, nanos);
        if (reply.matched() && !reply.injected()) {
            rtt.keepAlive(reply.rttNanos());
        }
        return reply;
    }

    AckTracker.Reply clientPong(int id, long nanos) {
        AckTracker.Reply reply = acks.clientPong(id, nanos);
        if (reply.matched()) {
            rtt.pong(reply.rttNanos(), nanos);
        }
        return reply;
    }

    SlotTracker.Change clientHeldItem(int slot) {
        return slots.clientSet(slot);
    }

    void sprinting(boolean value) {
        clientSprinting = value;
    }

    /** Sneaking from ENTITY_ACTION (before 1.21.6) or PLAYER_INPUT shift (1.21.6+). */
    void sneakingInput(boolean value, boolean fromPlayerInput) {
        if (fromPlayerInput == client.shiftInput()) {
            clientSneaking = value;
        }
    }

    /** VEHICLE_MOVE / STEER_VEHICLE: the client believes it is riding something. */
    void vehicleSignal(long nanos) {
        lastVehicleSignalNanos = nanos;
        anyVehicleSignal = true;
    }

    void clientCloseWindow(int id, long nanos) {
        containers.clientClose(id, nanos);
    }

    void windowClick(long nanos) {
        containers.click(nanos);
    }

    void playerLoaded() {
        loading = false;
    }

    /** C PLAYER_DIGGING RELEASE_USE_ITEM: provisional riptide exemption for 2 s + ping. Returns true if granted. */
    boolean releaseUseItem(long nanos) {
        releasedSinceUse = true;
        if (!signals.provisionalExemptionsAllowed(nanos)) {
            return false;
        }
        provisionalRiptide = true;
        provisionalRiptideUntil = nanos + 2 * SECOND + rtt.pingMillis(nanos) * 1_000_000L;
        return true;
    }

    /** C ENTITY_ACTION START_FLYING_WITH_ELYTRA: provisional gliding exemption (max 2 s). Returns true if granted. */
    boolean startElytra(long nanos) {
        if (!signals.provisionalExemptionsAllowed(nanos)) {
            return false;
        }
        provisionalGlide = true;
        provisionalGlideUntil = nanos + ELYTRA_PROVISIONAL_NANOS;
        return true;
    }

    void useItem() {
        useItemTick = tickEnds;
        releasedSinceUse = false;
    }

    void digStart(int bx, int by, int bz, long nanos) {
        digStart = new DigStart(bx, by, bz, tickEnds, nanos);
    }

    /** FINISH or CANCEL of the current dig; returns the START it ends, or null. */
    DigStart digEnd(int bx, int by, int bz) {
        DigStart start = digStart;
        digStart = null;
        return start != null && start.x() == bx && start.y() == by && start.z() == bz ? start : null;
    }

    /** Records the first brand only; returns true if this value was taken. */
    boolean brand(String value) {
        if (brand != null) {
            return false;
        }
        brand = value;
        return true;
    }

    // ---------------------------------------------------------------------------------------------
    // Read-only view for checks and telemetry
    // ---------------------------------------------------------------------------------------------

    public ClientInfo client() {
        return client;
    }

    public ExternalSignals signals() {
        return signals;
    }

    /** False for a hot-loaded session until its snapshot is applied. */
    public boolean seeded() {
        return seeded;
    }

    /** Own entity id, -1 when unknown (unseeded). */
    public int entityId() {
        return entityId;
    }

    public int vehicleId() {
        return vehicleId;
    }

    public int movesSinceSeed() {
        return movesSinceSeed;
    }

    /** Incremented on every JOIN_GAME/RESPAWN; candidates from an older epoch are stale. */
    public int epoch() {
        return epoch;
    }

    public String worldName() {
        return worldName;
    }

    public boolean positionKnown() {
        return positionKnown;
    }

    public boolean rotationKnown() {
        return rotationKnown;
    }

    public double x() {
        return x;
    }

    public double y() {
        return y;
    }

    public double z() {
        return z;
    }

    public float yaw() {
        return yaw;
    }

    public float pitch() {
        return pitch;
    }

    public boolean onGround() {
        return onGround;
    }

    /** Count of CLIENT_TICK_END packets since join (Event.tick). */
    public int tick() {
        return tickEnds;
    }

    public MoveHistory moves() {
        return moves;
    }

    public PositionHistory positions() {
        return positions;
    }

    public AckTracker acks() {
        return acks;
    }

    /** True when a change the server sent at {@code sentNanos} has been acknowledged by the client (or 5 s passed). */
    public boolean appliedByClient(long sentNanos) {
        return (acks.hasAck() && acks.ackedUpTo() - sentNanos >= 0)
                || moveClock.now - sentNanos >= APPLY_FALLBACK_NANOS;
    }

    public AckClock ackClock() {
        return currentClock;
    }

    public RttTracker rtt() {
        return rtt;
    }

    public TeleportTracker teleports() {
        return teleports;
    }

    public VelocityTracker velocities() {
        return velocities;
    }

    public ContainerTracker containers() {
        return containers;
    }

    public SlotTracker slots() {
        return slots;
    }

    public AttributeTracker attributes() {
        return attributes;
    }

    public EffectTracker effects() {
        return effects;
    }

    public Tracked<GameMode> gameMode() {
        return gameMode;
    }

    public Tracked<Abilities> abilities() {
        return abilities;
    }

    public Tracked<Boolean> inVehicle() {
        return inVehicle;
    }

    public boolean dead() {
        return dead;
    }

    public float tickRate() {
        return tickRate;
    }

    public boolean frozen() {
        return frozen;
    }

    public boolean tickRateNormal() {
        return Math.abs(tickRate - 20) < 1e-3 && !frozen;
    }

    /** Before PLAYER_LOADED (1.21.4+) or fewer than 3 position packets after JOIN_GAME/RESPAWN. */
    public boolean loading() {
        return loading || positionsSinceSpawn < TeleportTracker.GRACE_POSITIONS;
    }

    /** The last server teleport or rotation set a pitch outside [-90, 90] (until the next server rotation). */
    public boolean serverPitchOutOfRange() {
        return serverPitchOutOfRange;
    }

    public boolean sprinting() {
        return clientSprinting;
    }

    public boolean sneaking() {
        return clientSneaking;
    }

    /** Hand active in the applied metadata (the client is using an item). */
    public boolean handActiveApplied() {
        Integer hands = handState.applied();
        return hands != null && (hands & HAND_ACTIVE) != 0;
    }

    /**
     * Hand active in the applied metadata and in every change sent since: the server has not ended the item use
     * (completion, slot change), so the client, whose use the server's metadata controls, is still using it.
     */
    public boolean handActiveSteady() {
        return handActiveApplied() && !handState.anyMatch(h -> (h & HAND_ACTIVE) == 0);
    }

    /** Tick of the last C USE_ITEM, {@link Integer#MIN_VALUE} if none. */
    public int useItemTick() {
        return useItemTick;
    }

    public boolean releasedSinceUseItem() {
        return releasedSinceUse;
    }

    public DigStart digStart() {
        return digStart;
    }

    public String brand() {
        return brand;
    }

    public boolean flyingLenient() {
        return abilities.anyMatch(Abilities::flying);
    }

    public boolean allowFlightLenient() {
        return abilities.anyMatch(Abilities::allowFlight);
    }

    public boolean creativeOrSpectatorLenient() {
        return gameMode.anyMatch(m -> m == GameMode.GAME_MODE_CREATIVE || m == GameMode.GAME_MODE_SPECTATOR);
    }

    public boolean inVehicleLenient(long nowNanos) {
        return inVehicle.anyMatch(Boolean::booleanValue)
                || (anyVehicleSignal && nowNanos - lastVehicleSignalNanos < VEHICLE_SIGNAL_GRACE_NANOS);
    }

    private boolean inVehicleLenient() {
        return inVehicleLenient(moveClock.now);
    }

    public boolean glidingLenient() {
        return provisionalGlide || entityFlags.anyMatch(f -> (f & ENTITY_FLAG_GLIDING) != 0);
    }

    public boolean riptideLenient() {
        return provisionalRiptide || handState.anyMatch(h -> (h & HAND_RIPTIDE) != 0);
    }

    /**
     * The common exemption of local movement checks (CHECKS 1.1) for the newest packet, or null when the
     * checks may run.
     */
    public Exemption movementExemption(long nowNanos) {
        if (signals.detectionExempt()) {
            return Exemption.EXEMPT_PLAYER;
        }
        if (!client.tickClient()) {
            return Exemption.NOT_TICK_CLIENT;
        }
        if (!seeded) {
            return Exemption.UNSEEDED;
        }
        if (movesSinceSeed < WARMUP_MOVES) {
            return Exemption.WARMING_UP;
        }
        if (!gameMode.known() || !abilities.known() || !inVehicle.known() || !attributes.known() || !effects.known()
                || !positionKnown) {
            return Exemption.STATE_UNKNOWN;
        }
        if (creativeOrSpectatorLenient()) {
            return Exemption.GAME_MODE;
        }
        if (dead) {
            return Exemption.DEAD;
        }
        if (inVehicleLenient(nowNanos)) {
            return Exemption.VEHICLE;
        }
        if (flyingLenient() || (everFlying && nowNanos - lastFlyingNanos < FLYING_GRACE_NANOS)) {
            return Exemption.FLYING;
        }
        if (glidingLenient() || signals.envRecent(EnvFlag.ENV_FLAG_GLIDING_VALUE, ENV_WINDOW_NANOS, nowNanos)) {
            return Exemption.GLIDING;
        }
        if (riptideLenient() || signals.envRecent(EnvFlag.ENV_FLAG_RIPTIDING_VALUE, ENV_WINDOW_NANOS, nowNanos)) {
            return Exemption.RIPTIDE;
        }
        if (effects.present(EffectTracker.LEVITATION) || effects.present(EffectTracker.SLOW_FALLING)) {
            return Exemption.LEVITATION;
        }
        if (teleports.pending()) {
            return Exemption.TELEPORT;
        }
        if (velocities.pending()) {
            return Exemption.VELOCITY;
        }
        if (loading()) {
            return Exemption.LOADING;
        }
        if (!tickRateNormal()) {
            return Exemption.TICK_RATE;
        }
        Move newest = moves.get(0);
        if (newest != null && newest.cancelled() && previousCancelled) {
            return Exemption.CANCELLED;
        }
        if (signals.envRecent(ENV_EXEMPT_MASK, ENV_WINDOW_NANOS, nowNanos)) {
            return Exemption.ENVIRONMENT;
        }
        return null;
    }
}
