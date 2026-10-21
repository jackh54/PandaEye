package dev.pandascript.pandaeye.paper.session;

import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.common.util.TokenBucket;
import dev.pandascript.pandaeye.paper.check.PlayerChecks;
import dev.pandascript.pandaeye.paper.state.Baseline;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import org.bukkit.entity.Player;

import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ConcurrentLinkedQueue;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

/**
 * One player's session on this server: created on S JOIN_GAME (or during enable for hot-loaded players),
 * keyed by the PacketEvents {@link User} identity. The {@link PacketState} and checks belong to the player's
 * netty thread; everything other threads touch is volatile, atomic or a concurrent queue.
 */
public final class Session {

    private static final double CONFIRMATIONS_PER_SECOND = 2;
    private static final int MAX_OUTSTANDING_CONFIRMATIONS = 4;

    private final User user;
    private final UUID uuid;
    private final String name;
    private final boolean hotLoaded;
    private final ClientInfo client;
    private final SessionSignals signals;
    private final PacketState state;
    private final UUID id = UUID.randomUUID();
    private final long createdNanos;
    private final AtomicReference<Baseline> pendingBaseline = new AtomicReference<>();
    private final ConcurrentLinkedQueue<double[]> bukkitVelocities = new ConcurrentLinkedQueue<>();
    private final AtomicBoolean closed = new AtomicBoolean();
    private final AtomicInteger outstandingConfirmations = new AtomicInteger();
    private final TokenBucket confirmationRate;

    private PacketProcessor processor;
    private volatile Player player;
    private volatile long lastPacketNanos;
    private volatile long lastAttackedNanos = Long.MIN_VALUE;
    private volatile long lastFlagNanos = Long.MIN_VALUE;
    private volatile long watchUntilNanos = Long.MIN_VALUE;
    private volatile long lastBukkitVelocityNanos = Long.MIN_VALUE;
    private volatile SetbackTarget setbackTarget;
    private volatile String networkSession = "";
    private volatile boolean proxySessionAccepted;
    private volatile int publishedTick;
    private volatile boolean publishedInVehicle;
    private volatile double publishedScale = 1;
    private volatile Set<Mitigation> mitigations = Set.of();

    public Session(User user, UUID uuid, String name, boolean hotLoaded, ClientInfo client, SessionSignals signals,
                   long nowNanos) {
        this.user = user;
        this.uuid = uuid;
        this.name = name;
        this.hotLoaded = hotLoaded;
        this.client = client;
        this.signals = signals;
        this.state = new PacketState(client, signals);
        this.createdNanos = nowNanos;
        this.lastPacketNanos = nowNanos;
        this.confirmationRate = new TokenBucket(CONFIRMATIONS_PER_SECOND, CONFIRMATIONS_PER_SECOND, nowNanos);
    }

    /** Called once by the factory before the session is published. */
    void attach(PacketProcessor packetProcessor) {
        if (processor != null) {
            throw new IllegalStateException("already attached");
        }
        processor = packetProcessor;
    }

    /** Netty thread, before every packet: applies inputs handed over by other threads. */
    public void beforePacket(long nanos) {
        lastPacketNanos = nanos;
        Baseline baseline = pendingBaseline.getAndSet(null);
        if (baseline != null) {
            processor.applyBaseline(baseline, nanos);
        }
        double[] velocity;
        while ((velocity = bukkitVelocities.poll()) != null) {
            processor.velocity(velocity[0], velocity[1], velocity[2], VelocityTracker.Source.BUKKIT, (long) velocity[3]);
        }
    }

    /** Entity thread: the hot-load snapshot. */
    public void publishBaseline(Baseline baseline) {
        pendingBaseline.set(baseline);
    }

    /** Entity thread: a Bukkit PlayerVelocityEvent seen at MONITOR. */
    public void bukkitVelocity(double vx, double vy, double vz, long nanos) {
        lastBukkitVelocityNanos = nanos;
        bukkitVelocities.offer(new double[]{vx, vy, vz, nanos});
    }

    /** Confirmation limits: at most 4 outstanding and 2 per second per player. */
    public boolean tryReserveConfirmation(long nowNanos) {
        if (outstandingConfirmations.incrementAndGet() > MAX_OUTSTANDING_CONFIRMATIONS) {
            outstandingConfirmations.decrementAndGet();
            return false;
        }
        boolean admitted;
        synchronized (confirmationRate) {
            admitted = confirmationRate.tryAcquire(nowNanos, 1);
        }
        if (!admitted) {
            outstandingConfirmations.decrementAndGet();
        }
        return admitted;
    }

    public void releaseConfirmation() {
        outstandingConfirmations.decrementAndGet();
    }

    /** Marks the session closed; returns true for the first caller only. */
    public boolean close() {
        return closed.compareAndSet(false, true);
    }

    public boolean closed() {
        return closed.get();
    }

    public User user() {
        return user;
    }

    public UUID uuid() {
        return uuid;
    }

    public String name() {
        return name;
    }

    public boolean hotLoaded() {
        return hotLoaded;
    }

    public boolean bedrock() {
        return signals.bedrock();
    }

    public ClientInfo client() {
        return client;
    }

    /** Random per real join (PlayerJoin.session). */
    public UUID id() {
        return id;
    }

    public long createdNanos() {
        return createdNanos;
    }

    public SessionSignals signals() {
        return signals;
    }

    /** Netty thread only. */
    public PacketState state() {
        return state;
    }

    /** Netty thread only. */
    public PacketProcessor processor() {
        return processor;
    }

    public PlayerChecks checks() {
        return processor.checks();
    }

    public Player player() {
        return player;
    }

    public void player(Player value) {
        player = value;
    }

    public long lastPacketNanos() {
        return lastPacketNanos;
    }

    public long lastAttackedNanos() {
        return lastAttackedNanos;
    }

    /** Any thread: another player attacked this one. */
    public void attacked(long nanos) {
        lastAttackedNanos = nanos;
    }

    public long lastFlagNanos() {
        return lastFlagNanos;
    }

    public void flagged(long nanos) {
        lastFlagNanos = nanos;
    }

    public long watchUntilNanos() {
        return watchUntilNanos;
    }

    public void watchUntil(long nanos) {
        watchUntilNanos = nanos;
    }

    public long lastBukkitVelocityNanos() {
        return lastBukkitVelocityNanos;
    }

    public SetbackTarget setbackTarget() {
        return setbackTarget;
    }

    public void setbackTarget(SetbackTarget target) {
        setbackTarget = target;
    }

    public String networkSession() {
        return networkSession;
    }

    /** Netty thread: accepts a verified proxy session once per connection; returns true if accepted now. */
    public boolean acceptProxySession(String session) {
        if (proxySessionAccepted) {
            return false;
        }
        proxySessionAccepted = true;
        networkSession = session;
        return true;
    }

    /** Netty-published summary other players' threads may read (attack target info, env events). */
    public void publish(int tick, boolean inVehicle, double scale) {
        publishedTick = tick;
        publishedInVehicle = inVehicle;
        publishedScale = scale;
    }

    public int publishedTick() {
        return publishedTick;
    }

    public boolean publishedInVehicle() {
        return publishedInVehicle;
    }

    public double publishedScale() {
        return publishedScale;
    }

    public Set<Mitigation> mitigations() {
        return mitigations;
    }

    public void mitigations(Set<Mitigation> value) {
        mitigations = Set.copyOf(value);
    }
}
