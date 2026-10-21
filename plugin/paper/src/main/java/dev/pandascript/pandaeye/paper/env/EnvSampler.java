package dev.pandascript.pandaeye.paper.env;

import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.EnvSnapshot;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import dev.pandascript.pandaeye.protocol.v1.EnvFlag;
import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.bukkit.Location;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.entity.Player;
import org.bukkit.util.BoundingBox;

import java.util.concurrent.atomic.LongAdder;
import java.util.function.BooleanSupplier;
import java.util.function.Consumer;

/**
 * The per-player environment sampler (ARCHITECTURE 4.4): every 10 ticks on the player's region thread it
 * computes {@code EnvFlag} bits from cheap API calls, publishes an {@link EnvSnapshot}, sends an EnvSample on
 * change and at least every 5 s, and re-evaluates the bypass permission every 30 s. When the average cost
 * exceeds 20 microseconds, players that did not move sample at 1 Hz.
 */
public final class EnvSampler {

    public static final long PERIOD_TICKS = 10;
    static final long BUDGET_NANOS = 20_000;
    private static final long SEND_INTERVAL_NANOS = 5_000_000_000L;
    private static final long BYPASS_INTERVAL_NANOS = 30_000_000_000L;

    private final Telemetry telemetry;
    private final ExtendedReach reach;
    private final BooleanSupplier active;
    private final LongAdder samples = new LongAdder();
    private final LongAdder costNanos = new LongAdder();
    private volatile double averageCostNanos;

    public EnvSampler(Telemetry telemetry, ExtendedReach reach, BooleanSupplier active) {
        this.telemetry = telemetry;
        this.reach = reach;
        this.active = active;
    }

    /** The repeating task body for one player. */
    public Consumer<ScheduledTask> task(Session session, Player player) {
        return new PlayerTask(session, player);
    }

    public long samples() {
        return samples.sum();
    }

    /** Average sampling cost in nanoseconds (exponentially weighted). */
    public double averageCostNanos() {
        return averageCostNanos;
    }

    private final class PlayerTask implements Consumer<ScheduledTask> {

        private final Session session;
        private final Player player;
        private long lastRunNanos;
        private long lastSentNanos = Long.MIN_VALUE;
        private long lastBypassNanos = Long.MIN_VALUE;
        private int lastFlags = -1;
        private double lastX = Double.NaN;
        private double lastZ = Double.NaN;
        private boolean skipped;

        PlayerTask(Session session, Player player) {
            this.session = session;
            this.player = player;
        }

        @Override
        public void accept(ScheduledTask task) {
            if (!active.getAsBoolean() || session.closed()) {
                task.cancel();
                return;
            }
            long start = System.nanoTime();
            long interval = lastRunNanos == 0 ? PERIOD_TICKS * 50_000_000L : start - lastRunNanos;
            lastRunNanos = start;
            Location location = player.getLocation();
            boolean idle = location.getX() == lastX && location.getZ() == lastZ;
            if (averageCostNanos > BUDGET_NANOS && idle && !skipped) {
                skipped = true;
                return;
            }
            skipped = false;
            if (lastBypassNanos == Long.MIN_VALUE || start - lastBypassNanos >= BYPASS_INTERVAL_NANOS) {
                lastBypassNanos = start;
                session.signals().bypass(player.hasPermission("pandaeye.bypass"));
            }
            int flags = flags(player, location);
            float tickMs = (float) (interval / 1e6 / PERIOD_TICKS);
            EnvSnapshot snapshot = new EnvSnapshot(flags, location.getX(), location.getY(), location.getZ(),
                    location.getWorld().getUID(), tickMs, start);
            session.signals().publish(snapshot);
            if (flags != lastFlags || start - lastSentNanos >= SEND_INTERVAL_NANOS) {
                lastFlags = flags;
                lastSentNanos = start;
                telemetry.envSample(session, snapshot);
            }
            lastX = location.getX();
            lastZ = location.getZ();
            long cost = System.nanoTime() - start;
            samples.increment();
            costNanos.add(cost);
            averageCostNanos = averageCostNanos * 0.99 + cost * 0.01;
        }
    }

    int flags(Player player, Location location) {
        int flags = 0;
        if (player.isInWater()) {
            flags |= EnvFlag.ENV_FLAG_IN_WATER_VALUE;
        }
        if (player.isInLava()) {
            flags |= EnvFlag.ENV_FLAG_IN_LAVA_VALUE;
        }
        if (player.isClimbing()) {
            flags |= EnvFlag.ENV_FLAG_CLIMBING_VALUE;
        }
        if (player.isGliding()) {
            flags |= EnvFlag.ENV_FLAG_GLIDING_VALUE;
        }
        if (player.isRiptiding()) {
            flags |= EnvFlag.ENV_FLAG_RIPTIDING_VALUE;
        }
        if (player.isSwimming()) {
            flags |= EnvFlag.ENV_FLAG_SWIMMING_VALUE;
        }
        if (player.isInsideVehicle()) {
            flags |= EnvFlag.ENV_FLAG_IN_VEHICLE_VALUE;
        }
        if (player.isInBubbleColumn()) {
            flags |= EnvFlag.ENV_FLAG_IN_BUBBLE_COLUMN_VALUE;
        }
        World world = location.getWorld();
        int bx = location.getBlockX();
        int bz = location.getBlockZ();
        double y = location.getY();
        Material feet = world.getBlockAt(bx, (int) Math.floor(y), bz).getType();
        Material support = world.getBlockAt(bx, (int) Math.floor(y - 0.5), bz).getType();
        Material below = world.getBlockAt(bx, (int) Math.floor(y) - 1, bz).getType();
        flags |= surface(support) | surface(below);
        if (feet == Material.COBWEB) {
            flags |= EnvFlag.ENV_FLAG_IN_WEB_VALUE;
        }
        if (feet == Material.POWDER_SNOW || support == Material.POWDER_SNOW) {
            flags |= EnvFlag.ENV_FLAG_IN_POWDER_SNOW_VALUE;
        }
        if (BlockKinds.stuck(feet) || BlockKinds.stuck(support)) {
            flags |= EnvFlag.ENV_FLAG_STUCK_VALUE;
        }
        if (BlockKinds.soulSpeed(feet)) {
            flags |= EnvFlag.ENV_FLAG_ON_SOUL_SPEED_BLOCK_VALUE;
        }
        BoundingBox box = player.getBoundingBox();
        if (world.hasCollisionsIn(box.clone().expand(1))) {
            flags |= EnvFlag.ENV_FLAG_NEAR_SOLID_VALUE;
        }
        double top = box.getMaxY();
        if (world.hasCollisionsIn(new BoundingBox(location.getX() - 0.3, top, location.getZ() - 0.3,
                location.getX() + 0.3, top + 1.0, location.getZ() + 0.3))) {
            flags |= EnvFlag.ENV_FLAG_LOW_CEILING_VALUE;
        }
        if (!world.getNearbyEntities(box.clone().expand(1.5),
                entity -> entity != player && Collidables.pushesOrCollides(entity)).isEmpty()) {
            flags |= EnvFlag.ENV_FLAG_NEAR_ENTITY_VALUE;
        }
        if (reach.test(player.getInventory().getItemInMainHand())) {
            flags |= EnvFlag.ENV_FLAG_EXTENDED_REACH_ITEM_VALUE;
        }
        return flags;
    }

    private static int surface(Material type) {
        if (BlockKinds.ice(type)) {
            return EnvFlag.ENV_FLAG_ON_ICE_VALUE;
        }
        if (type == Material.SLIME_BLOCK) {
            return EnvFlag.ENV_FLAG_ON_SLIME_VALUE;
        }
        if (type == Material.HONEY_BLOCK) {
            return EnvFlag.ENV_FLAG_ON_HONEY_VALUE;
        }
        if (BlockKinds.soulSpeed(type)) {
            return EnvFlag.ENV_FLAG_ON_SOUL_SPEED_BLOCK_VALUE;
        }
        return 0;
    }
}
