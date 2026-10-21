package dev.pandascript.pandaeye.paper.platform;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.junit.jupiter.api.Test;

import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Consumer;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Every scheduled body runs behind the active gate and inside a catch-all. */
class SchedulersGuardTest {

    private final List<String> warnings = new ArrayList<>();
    private final AtomicBoolean active = new AtomicBoolean(true);
    private final Schedulers schedulers = new Schedulers(null, active::get, new RateLimitedLog(new Log() {
        @Override
        public void info(String message) {
        }

        @Override
        public void warn(String message) {
            warnings.add(message);
        }

        @Override
        public void warn(String message, Throwable error) {
            warnings.add(message + " " + error);
        }

        @Override
        public void debug(String message) {
        }
    }, 1));

    private static ScheduledTask handle(AtomicInteger cancels) {
        return (ScheduledTask) Proxy.newProxyInstance(SchedulersGuardTest.class.getClassLoader(),
                new Class<?>[]{ScheduledTask.class}, (proxy, method, args) -> {
                    if (method.getName().equals("cancel")) {
                        cancels.incrementAndGet();
                        return ScheduledTask.CancelledState.CANCELLED_BY_CALLER;
                    }
                    if (method.getName().equals("hashCode")) {
                        return 1;
                    }
                    throw new UnsupportedOperationException(method.getName());
                });
    }

    @Test
    void aThrowingBodyIsReportedNotThrown() {
        Runnable task = schedulers.guard("global", () -> {
            throw new IllegalStateException("boom");
        });
        assertDoesNotThrow(task::run);
        Consumer<ScheduledTask> repeating = schedulers.guard("async repeat", t -> {
            throw new LinkageError("class loader closed");
        });
        assertDoesNotThrow(() -> repeating.accept(handle(new AtomicInteger())));
        assertEquals(2, warnings.size(), warnings.toString());
        assertTrue(warnings.get(0).contains("boom"));
    }

    @Test
    void bodiesDoNotRunOnceInactiveAndRepeatingTasksCancelThemselves() {
        AtomicInteger runs = new AtomicInteger();
        AtomicInteger cancels = new AtomicInteger();
        Runnable once = schedulers.guard("global", runs::incrementAndGet);
        Consumer<ScheduledTask> repeating = schedulers.guard("entity repeat", t -> runs.incrementAndGet());
        once.run();
        repeating.accept(handle(cancels));
        assertEquals(2, runs.get());
        active.set(false);
        once.run();
        repeating.accept(handle(cancels));
        assertEquals(2, runs.get());
        assertEquals(1, cancels.get());
    }
}
