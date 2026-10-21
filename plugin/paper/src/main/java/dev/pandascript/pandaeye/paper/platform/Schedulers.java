package dev.pandascript.pandaeye.paper.platform;

import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.bukkit.Bukkit;
import org.bukkit.entity.Entity;
import org.bukkit.plugin.Plugin;

import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.function.Consumer;

/**
 * The only way PandaEye schedules work: Paper's regionised schedulers (present on Paper and Folia), never the
 * BukkitScheduler. Entity and region schedulers have no {@code cancelTasks(Plugin)}, so every entity task
 * handle is kept per owner and cancelled on quit and on disable; global and async tasks are cancelled with
 * {@code cancelTasks(plugin)}.
 */
public final class Schedulers {

    private final Plugin plugin;
    private final Map<UUID, Set<ScheduledTask>> entityTasks = new ConcurrentHashMap<>();

    public Schedulers(Plugin plugin) {
        this.plugin = plugin;
    }

    /**
     * Runs once on the entity's thread after {@code delayTicks} (0 = next tick). Returns false when the entity
     * is retired (the retired callback is not invoked then).
     */
    public boolean runOnEntity(Entity entity, UUID owner, Consumer<ScheduledTask> task, Runnable retired, long delayTicks) {
        Set<ScheduledTask> tasks = tasksOf(owner);
        Consumer<ScheduledTask> wrapped = handle -> {
            try {
                task.accept(handle);
            } finally {
                tasks.remove(handle);
            }
        };
        ScheduledTask handle = delayTicks <= 0
                ? entity.getScheduler().run(plugin, wrapped, retired)
                : entity.getScheduler().runDelayed(plugin, wrapped, retired, delayTicks);
        if (handle == null) {
            return false;
        }
        if (!handle.isCancelled() && handle.getExecutionState() != ScheduledTask.ExecutionState.FINISHED) {
            tasks.add(handle);
        }
        return true;
    }

    /** Repeats on the entity's thread until cancelled, retired, or the owner's tasks are cancelled. */
    public ScheduledTask repeatOnEntity(Entity entity, UUID owner, Consumer<ScheduledTask> task, Runnable retired,
                                        long initialDelayTicks, long periodTicks) {
        ScheduledTask handle = entity.getScheduler().runAtFixedRate(plugin, task, retired, initialDelayTicks, periodTicks);
        if (handle != null) {
            tasksOf(owner).add(handle);
        }
        return handle;
    }

    public void runGlobal(Runnable task) {
        Bukkit.getGlobalRegionScheduler().execute(plugin, task);
    }

    public ScheduledTask repeatGlobal(Consumer<ScheduledTask> task, long initialDelayTicks, long periodTicks) {
        return Bukkit.getGlobalRegionScheduler().runAtFixedRate(plugin, task, initialDelayTicks, periodTicks);
    }

    public ScheduledTask repeatAsync(Consumer<ScheduledTask> task, long initialDelay, long period, TimeUnit unit) {
        return Bukkit.getAsyncScheduler().runAtFixedRate(plugin, task, initialDelay, period, unit);
    }

    /** Cancels every entity task of one owner (player quit). */
    public void cancel(UUID owner) {
        Set<ScheduledTask> tasks = entityTasks.remove(owner);
        if (tasks != null) {
            for (ScheduledTask task : tasks) {
                task.cancel();
            }
        }
    }

    /** Cancels everything this plugin scheduled (disable). */
    public void cancelAll() {
        for (UUID owner : entityTasks.keySet()) {
            cancel(owner);
        }
        Bukkit.getGlobalRegionScheduler().cancelTasks(plugin);
        Bukkit.getAsyncScheduler().cancelTasks(plugin);
    }

    private Set<ScheduledTask> tasksOf(UUID owner) {
        return entityTasks.computeIfAbsent(owner, key -> ConcurrentHashMap.newKeySet());
    }
}
