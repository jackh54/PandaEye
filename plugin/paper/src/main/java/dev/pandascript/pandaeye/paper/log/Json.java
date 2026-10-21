package dev.pandascript.pandaeye.paper.log;

import java.util.Locale;
import java.util.Map;

/** A minimal JSON object writer for the local log lines (no dependency, escapes control characters). */
public final class Json {

    private final StringBuilder out = new StringBuilder(256).append('{');
    private boolean first = true;

    public Json put(String key, String value) {
        key(key);
        string(value);
        return this;
    }

    public Json put(String key, long value) {
        key(key);
        out.append(value);
        return this;
    }

    public Json put(String key, double value) {
        key(key);
        if (Double.isFinite(value)) {
            out.append(String.format(Locale.ROOT, "%.6g", value));
        } else {
            out.append("null");
        }
        return this;
    }

    public Json put(String key, boolean value) {
        key(key);
        out.append(value);
        return this;
    }

    public Json put(String key, Map<String, Double> values) {
        key(key);
        Json nested = new Json();
        values.forEach(nested::put);
        out.append(nested);
        return this;
    }

    @Override
    public String toString() {
        return out + "}";
    }

    private void key(String key) {
        if (!first) {
            out.append(',');
        }
        first = false;
        string(key);
        out.append(':');
    }

    private void string(String value) {
        out.append('"');
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            switch (c) {
                case '"' -> out.append("\\\"");
                case '\\' -> out.append("\\\\");
                case '\n' -> out.append("\\n");
                case '\r' -> out.append("\\r");
                case '\t' -> out.append("\\t");
                default -> {
                    if (c < 0x20 || c == 0x7f) {
                        out.append(String.format(Locale.ROOT, "\\u%04x", (int) c));
                    } else {
                        out.append(c);
                    }
                }
            }
        }
        out.append('"');
    }
}
