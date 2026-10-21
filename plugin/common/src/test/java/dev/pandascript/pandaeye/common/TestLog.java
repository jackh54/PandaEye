package dev.pandascript.pandaeye.common;

import java.util.ArrayList;
import java.util.List;

/** Collects log lines for assertions. */
public final class TestLog implements Log {

    private final List<String> lines = new ArrayList<>();

    @Override
    public synchronized void info(String message) {
        lines.add("INFO " + message);
    }

    @Override
    public synchronized void warn(String message) {
        lines.add("WARN " + message);
    }

    @Override
    public synchronized void warn(String message, Throwable error) {
        lines.add("WARN " + message + " " + error);
    }

    @Override
    public synchronized void debug(String message) {
        lines.add("DEBUG " + message);
    }

    public synchronized List<String> lines() {
        return List.copyOf(lines);
    }

    public synchronized boolean contains(String fragment) {
        return lines.stream().anyMatch(line -> line.contains(fragment));
    }

    public synchronized String dump() {
        return String.join("\n", lines);
    }
}
