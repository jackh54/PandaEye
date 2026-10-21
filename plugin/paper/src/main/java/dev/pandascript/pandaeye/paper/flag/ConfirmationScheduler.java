package dev.pandascript.pandaeye.paper.flag;

import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.util.TokenBucket;
import dev.pandascript.pandaeye.paper.check.Candidate;
import dev.pandascript.pandaeye.paper.check.CandidateSink;
import dev.pandascript.pandaeye.paper.check.CheckHandle;
import dev.pandascript.pandaeye.paper.check.ConfirmContext;
import dev.pandascript.pandaeye.paper.check.Confirmation;
import dev.pandascript.pandaeye.paper.check.WorldView;
import dev.pandascript.pandaeye.paper.env.BukkitWorldView;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import dev.pandascript.pandaeye.paper.session.Session;
import org.bukkit.Bukkit;
import org.bukkit.GameMode;
import org.bukkit.World;
import org.bukkit.attribute.Attribute;
import org.bukkit.attribute.AttributeInstance;
import org.bukkit.entity.Player;

import java.util.List;
import java.util.concurrent.atomic.LongAdder;
import java.util.function.BooleanSupplier;

/**
 * The confirmation step (CHECKS 1.1): one tick after a candidate, on the player's entity thread, discard it
 * unless the world still matches, every position is in a loaded chunk owned by the current region, and (for
 * movement candidates) the authoritative exemptions do not apply; then run the check's confirmer. At most 4
 * outstanding and 2 per second per player, 200 per second server-wide; extra candidates are dropped and
 * counted.
 */
public final class ConfirmationScheduler {

    static final double GLOBAL_PER_SECOND = 200;
    private static final long RECENT_VELOCITY_NANOS = 2_000_000_000L;

    private final Schedulers schedulers;
    private final BooleanSupplier active;
    private final RateLimitedLog errors;
    private final TokenBucket global = new TokenBucket(GLOBAL_PER_SECOND, GLOBAL_PER_SECOND, System.nanoTime());
    private final LongAdder dropped = new LongAdder();
    private final LongAdder discarded = new LongAdder();

    public ConfirmationScheduler(Schedulers schedulers, BooleanSupplier active, RateLimitedLog errors) {
        this.schedulers = schedulers;
        this.active = active;
        this.errors = errors;
    }

    public CandidateSink forSession(Session session) {
        return (handle, candidate) -> submit(session, handle, candidate);
    }

    /** Candidates dropped by the limits (shown in /pandaeye status). */
    public long dropped() {
        return dropped.sum();
    }

    public long discarded() {
        return discarded.sum();
    }

    private boolean submit(Session session, CheckHandle handle, Candidate candidate) {
        long now = System.nanoTime();
        Player player = session.player();
        if (player == null || !session.tryReserveConfirmation(now)) {
            dropped.increment();
            return false;
        }
        boolean admitted;
        synchronized (global) {
            admitted = global.tryAcquire(now, 1);
        }
        if (!admitted || !schedulers.runOnEntity(player, session.uuid(),
                task -> run(session, player, handle, candidate), session::releaseConfirmation, 1)) {
            session.releaseConfirmation();
            dropped.increment();
            return false;
        }
        return true;
    }

    private void run(Session session, Player player, CheckHandle handle, Candidate candidate) {
        session.releaseConfirmation();
        if (!active.getAsBoolean() || session.closed()) {
            return;
        }
        try {
            switch (confirm(session, player, candidate)) {
                case Confirmation.Confirmed confirmed -> handle.confirmedByWorld(candidate,
                        confirmed.violation() == null ? candidate.violation() : confirmed.violation(), System.nanoTime());
                case Confirmation.Discarded ignored -> {
                    discarded.increment();
                    handle.discardedByWorld();
                }
            }
        } catch (Throwable t) {
            errors.warn("confirmation " + handle.descriptor().id(), "confirmation step failed", t);
        }
    }

    private static Confirmation confirm(Session session, Player player, Candidate candidate) {
        World world = player.getWorld();
        if (!world.getUID().equals(candidate.worldUid())) {
            return Confirmation.discarded("world changed");
        }
        for (ConfirmContext.Position position : candidate.positions()) {
            int chunkX = (int) Math.floor(position.x()) >> 4;
            int chunkZ = (int) Math.floor(position.z()) >> 4;
            if (!world.isChunkLoaded(chunkX, chunkZ)) {
                return Confirmation.discarded("chunk not loaded");
            }
            if (!Bukkit.isOwnedByCurrentRegion(world, chunkX, chunkZ)) {
                return Confirmation.discarded("position outside the player's region");
            }
        }
        if (candidate.movement()) {
            GameMode mode = player.getGameMode();
            if (player.isRiptiding() || player.isGliding() || player.isFlying() || player.isInsideVehicle()
                    || mode == GameMode.CREATIVE || mode == GameMode.SPECTATOR) {
                return Confirmation.discarded("authoritative exemption");
            }
            if (System.nanoTime() - session.lastBukkitVelocityNanos() < RECENT_VELOCITY_NANOS) {
                return Confirmation.discarded("velocity in the last 2 s");
            }
        }
        AttributeInstance scale = player.getAttribute(Attribute.SCALE);
        double scaleValue = scale == null ? 1 : scale.getValue();
        WorldView view = new BukkitWorldView(world, player);
        return candidate.confirmer().confirm(new ConfirmContext() {
            @Override
            public WorldView world() {
                return view;
            }

            @Override
            public List<Position> positions() {
                return candidate.positions();
            }

            @Override
            public double scale() {
                return scaleValue;
            }
        });
    }
}
