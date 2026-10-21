package dev.pandascript.pandaeye.paper.platform;

import dev.pandascript.pandaeye.common.Log;

import java.util.function.BooleanSupplier;
import java.util.logging.Level;
import java.util.logging.Logger;

/** {@link Log} over the plugin logger; adds no JUL handlers. */
public final class BukkitLog implements Log {

    private final Logger logger;
    private final BooleanSupplier debug;

    public BukkitLog(Logger logger, BooleanSupplier debug) {
        this.logger = logger;
        this.debug = debug;
    }

    @Override
    public void info(String message) {
        logger.info(message);
    }

    @Override
    public void warn(String message) {
        logger.warning(message);
    }

    @Override
    public void warn(String message, Throwable error) {
        logger.log(Level.WARNING, message, error);
    }

    @Override
    public void debug(String message) {
        if (debug.getAsBoolean()) {
            logger.info("[debug] " + message);
        }
    }
}
