package dev.pandascript.pandaeye.velocity;

import dev.pandascript.pandaeye.common.Log;
import org.slf4j.Logger;

import java.util.function.BooleanSupplier;

/** {@link Log} over the plugin's SLF4J logger. */
final class Slf4jLog implements Log {

    private final Logger logger;
    private final BooleanSupplier debug;

    Slf4jLog(Logger logger, BooleanSupplier debug) {
        this.logger = logger;
        this.debug = debug;
    }

    @Override
    public void info(String message) {
        logger.info(message);
    }

    @Override
    public void warn(String message) {
        logger.warn(message);
    }

    @Override
    public void warn(String message, Throwable error) {
        logger.warn(message, error);
    }

    @Override
    public void debug(String message) {
        if (debug.getAsBoolean()) {
            logger.info("[debug] " + message);
        }
    }
}
