package dev.pandascript.pandaeye.common.util;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.ThreadFactory;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Creates named daemon threads without a context class loader, so no thread (and no JDK thread a thread of
 * ours spawns, such as the HttpClient selector) pins the plugin class loader after a hot unload. Remembers
 * the threads it created so shutdown can join them.
 */
public final class DaemonThreads implements ThreadFactory {

    private final String name;
    private final boolean numbered;
    private final AtomicInteger counter = new AtomicInteger();
    private final List<Thread> created = new CopyOnWriteArrayList<>();

    private DaemonThreads(String name, boolean numbered) {
        this.name = name;
        this.numbered = numbered;
    }

    /** Threads all called {@code name}. */
    public static DaemonThreads named(String name) {
        return new DaemonThreads(name, false);
    }

    /** Threads called {@code prefix1}, {@code prefix2}, ... */
    public static DaemonThreads numbered(String prefix) {
        return new DaemonThreads(prefix, true);
    }

    @Override
    public Thread newThread(Runnable task) {
        String threadName = numbered ? name + counter.incrementAndGet() : name;
        Thread thread = new Thread(null, task, threadName, 0, false);
        thread.setDaemon(true);
        thread.setContextClassLoader(null);
        created.removeIf(t -> t.getState() == Thread.State.TERMINATED);
        created.add(thread);
        return thread;
    }

    /** Waits until every thread created by this factory has terminated or the deadline passes. */
    public boolean joinAll(long deadlineNanos) {
        for (Thread thread : created) {
            long remainingMillis = (deadlineNanos - System.nanoTime()) / 1_000_000L;
            if (remainingMillis <= 0) {
                return !anyAlive();
            }
            try {
                thread.join(remainingMillis);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return !anyAlive();
            }
        }
        return !anyAlive();
    }

    private boolean anyAlive() {
        for (Thread thread : created) {
            if (thread.isAlive()) {
                return true;
            }
        }
        return false;
    }
}
