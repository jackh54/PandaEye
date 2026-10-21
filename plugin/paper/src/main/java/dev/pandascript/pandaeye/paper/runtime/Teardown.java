package dev.pandascript.pandaeye.paper.runtime;

import java.util.function.Consumer;

/** Runs disable steps in isolation: a step that throws is reported and never skips the steps after it. */
final class Teardown {

    private final Consumer<String> warn;
    private int failures;

    Teardown(Consumer<String> warn) {
        this.warn = warn;
    }

    void step(String name, Runnable body) {
        try {
            body.run();
        } catch (Throwable t) {
            failures++;
            try {
                warn.accept("disable: could not " + name + ": " + t);
            } catch (Throwable ignored) {
                // nothing left to report to
            }
        }
    }

    int failures() {
        return failures;
    }
}
