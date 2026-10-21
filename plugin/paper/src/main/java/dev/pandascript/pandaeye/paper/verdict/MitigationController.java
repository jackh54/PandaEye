package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.enforce.Enforcer;
import dev.pandascript.pandaeye.paper.enforce.MitigationSource;
import dev.pandascript.pandaeye.paper.session.MitigationState;
import dev.pandascript.pandaeye.paper.session.Session;
import org.bukkit.entity.Player;

import java.util.Collection;
import java.util.UUID;
import java.util.concurrent.ThreadLocalRandom;
import java.util.function.BooleanSupplier;
import java.util.function.Consumer;
import java.util.function.Function;
import java.util.function.Supplier;

/**
 * Keeps the mitigation listeners registered exactly while mitigations may be enforced (ARCHITECTURE 4.8):
 * local mode live AND last-known cloud enforcement AND {@code mitigations.enabled}. Registration goes through
 * the enforcer ({@link Enforcer#registerMitigations}, a no-op on the shadow enforcer) on the global region
 * thread. When enforcement turns off, the listeners are removed and every active mitigation is cleared; the
 * listener also re-checks the gate on every event, so nothing is changed in between. Thread-safe.
 */
public final class MitigationController implements MitigationSource {

    private final Enforcement enforcement;
    private final Supplier<PluginConfig> config;
    private final Function<UUID, Session> sessionOf;
    private final Supplier<? extends Collection<Session>> allSessions;
    private final Consumer<Runnable> globalThread;
    private final BooleanSupplier active;
    private final Log log;
    private final RateLimitedLog errors;
    private final Object lock = new Object();
    private Enforcer.Registration registration = Enforcer.Registration.NONE;
    private boolean scheduled;
    private volatile boolean closed;

    public MitigationController(Enforcement enforcement, Supplier<PluginConfig> config, Function<UUID, Session> sessionOf,
                                Supplier<? extends Collection<Session>> allSessions, Consumer<Runnable> globalThread,
                                BooleanSupplier active, Log log, RateLimitedLog errors) {
        this.enforcement = enforcement;
        this.config = config;
        this.sessionOf = sessionOf;
        this.allSessions = allSessions;
        this.globalThread = globalThread;
        this.active = active;
        this.log = log;
        this.errors = errors;
    }

    /** True while mitigations may be applied at all. */
    public boolean allowed() {
        return !closed && enforcement.enabled() && config.get().mitigations();
    }

    public boolean registered() {
        synchronized (lock) {
            return registration.active();
        }
    }

    /** Any thread: re-evaluates the gate (enable, RemoteConfig change, MITIGATE) and (un)registers on the global thread. */
    public void refresh() {
        boolean allowed = allowed();
        if (!allowed) {
            clearAll();
        }
        synchronized (lock) {
            if (closed || scheduled || allowed == registration.active()) {
                return;
            }
            scheduled = true;
        }
        globalThread.accept(this::apply);
    }

    /** Disable: unregisters (if still registered) and forgets everything. */
    public void close() {
        closed = true;
        synchronized (lock) {
            registration.close();
            registration = Enforcer.Registration.NONE;
        }
    }

    @Override
    public MitigationState state(Player player) {
        if (!active.getAsBoolean() || !allowed()) {
            return null;
        }
        Session session = sessionOf.apply(player.getUniqueId());
        if (session == null) {
            return null;
        }
        MitigationState state = session.mitigation();
        return state.active() ? state : null;
    }

    @Override
    public void failed(String site, Throwable error) {
        errors.warn(site, "mitigation listener failed", error);
    }

    private void apply() {
        try {
            boolean allowed = active.getAsBoolean() && allowed();
            synchronized (lock) {
                scheduled = false;
                if (closed) {
                    return;
                }
                if (allowed && !registration.active()) {
                    registration = enforcement.forFeature(config.get().mitigations())
                            .registerMitigations(this, () -> ThreadLocalRandom.current().nextDouble());
                    if (registration.active()) {
                        log.info("mitigation listeners registered (live mode, cloud enforcement on)");
                    }
                } else if (!allowed && registration.active()) {
                    registration.close();
                    registration = Enforcer.Registration.NONE;
                    log.info("mitigation listeners removed: enforcement is off");
                }
            }
            if (allowed != allowed()) {
                refresh();
            }
        } catch (Throwable t) {
            errors.warn("mitigations", "could not update the mitigation listeners", t);
        }
    }

    private void clearAll() {
        for (Session session : allSessions.get()) {
            if (session.mitigation().active()) {
                session.mitigation(null);
            }
        }
    }
}
