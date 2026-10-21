package dev.pandascript.pandaeye.paper.platform;

import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.bukkit.Bukkit;
import org.bukkit.entity.Entity;
import org.bukkit.plugin.Plugin;

import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.function.BooleanSupplier;
import java.util.function.Consumer;

/**
 * The only way PandaEye schedules work: Paper's regionised schedulers (present on Paper and Folia), never the
 * BukkitScheduler. Entity and region schedulers have no {@code cancelTasks(Plugin)}, so every entity task
 * handle is kept per owner and cancelled on quit and on disable; global and async tasks are cancelled with
 * {@code cancelTasks(plugin)}. Every task body runs behind the active gate (a repeating task cancels itself
 * once the plugin is inactive) and inside a catch-all that reports to the rate-limited error log, so no
 * PandaEye task can throw into a region thread.
 */
public final class Schedulers {

    private final Plugin plugin;
    private final BooleanSupplier active;
    private final RateLimitedLog errors;
    private final Map<UUID, Set<ScheduledTask>> entityTasks = new ConcurrentHashMap<>();

    /** Ungated, errors swallowed (tests). */
    public Schedulers(Plugin plugin) {
        this(plugin, () -> true, null);
    }

    public Schedulers(Plugin plugin, BooleanSupplier active, RateLimitedLog errors) {
        this.plugin = plugin;
        this.active = active;
        this.errors = errors;
    }

    /** Gate + catch-all around a one-shot body. */
    Runnable guard(String site, Runnable body) {
        return () -> {
            if (!active.getAsBoolean()) {
                return;
            }
            try {
                body.run();
            } catch (Throwable t) {
                failed(site, t);
            }
        };
    }

    /** Gate + catch-all around a task body; a repeating task is cancelled once the plugin is inactive. */
    Consumer<ScheduledTask> guard(String site, Consumer<ScheduledTask> body) {
        return handle -> {
            if (!active.getAsBoolean()) {
                handle.cancel();
                return;
            }
            try {
                body.accept(handle);
            } catch (Throwable t) {
                failed(site, t);
            }
        };
    }

    /** Catch-all only (cleanup callbacks such as an entity's retired callback run even when inactive). */
    private Runnable quiet(String site, Runnable body) {
        if (body == null) {
            return null;
        }
        return () -> {
            try {
                body.run();
            } catch (Throwable t) {
                failed(site, t);
            }
        };
    }

    private void failed(String site, Throwable t) {
        if (errors != null) {
            errors.warn("task " + site, "scheduled task failed", t);
        }
    }

    /**
     * Runs once on the entity's thread after {@code delayTicks} (0 = next tick). Returns false when the entity
     * is retired (the retired callback is not invoked then).
     */
    public boolean runOnEntity(Entity entity, UUID owner, Consumer<ScheduledTask> task, Runnable retired, long delayTicks) {
        Set<ScheduledTask> tasks = tasksOf(owner);
        Consumer<ScheduledTask> guarded = guard("entity", task);
        Consumer<ScheduledTask> wrapped = handle -> {
            try {
                guarded.accept(handle);
            } finally {
                tasks.remove(handle);
            }
        };
        Runnable retiredQuiet = quiet("entity retired", retired);
        ScheduledTask handle = delayTicks <= 0
                ? entity.getScheduler().run(plugin, wrapped, retiredQuiet)
                : entity.getScheduler().runDelayed(plugin, wrapped, retiredQuiet, delayTicks);
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
        ScheduledTask handle = entity.getScheduler().runAtFixedRate(plugin, guard("entity repeat", task),
                quiet("entity retired", retired), initialDelayTicks, periodTicks);
        if (handle != null) {
            tasksOf(owner).add(handle);
        }
        return handle;
    }

    public void runGlobal(Runnable task) {
        Bukkit.getGlobalRegionScheduler().execute(plugin, guard("global", task));
    }

    public ScheduledTask repeatGlobal(Consumer<ScheduledTask> task, long initialDelayTicks, long periodTicks) {
        return Bukkit.getGlobalRegionScheduler().runAtFixedRate(plugin, guard("global repeat", task),
                initialDelayTicks, periodTicks);
    }

    public ScheduledTask repeatAsync(Consumer<ScheduledTask> task, long initialDelay, long period, TimeUnit unit) {
        return Bukkit.getAsyncScheduler().runAtFixedRate(plugin, guard("async repeat", task), initialDelay, period,
                unit);
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
