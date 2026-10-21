package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.AtomicFiles;

import java.io.IOException;
import java.nio.file.Path;
import java.util.Properties;

/** {@code state.properties}: the reserved seq block and whether the last shutdown was clean. */
final class StateStore {

    private static final String SEQ_BLOCK_END = "seq.block-end";
    private static final String CLEAN_SHUTDOWN = "clean-shutdown";

    private final Path file;
    private final Log log;
    private final Properties properties;

    StateStore(Path file, Log log) {
        this.file = file;
        this.log = log;
        Properties loaded;
        try {
            loaded = AtomicFiles.readProperties(file);
        } catch (IOException | IllegalArgumentException e) {
            log.warn("could not read " + file.getFileName() + ", starting a new seq block above 0: " + e);
            loaded = new Properties();
        }
        this.properties = loaded;
    }

    synchronized long seqBlockEnd() {
        try {
            return Math.max(0, Long.parseLong(properties.getProperty(SEQ_BLOCK_END, "0").trim()));
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    synchronized boolean wasCleanShutdown() {
        return Boolean.parseBoolean(properties.getProperty(CLEAN_SHUTDOWN, "true"));
    }

    /** Persists a new block end and marks the process as running (not cleanly shut down). */
    synchronized boolean saveSeqBlockEnd(long blockEnd) {
        properties.setProperty(SEQ_BLOCK_END, Long.toString(blockEnd));
        properties.setProperty(CLEAN_SHUTDOWN, "false");
        return save();
    }

    synchronized void markCleanShutdown() {
        properties.setProperty(CLEAN_SHUTDOWN, "true");
        save();
    }

    private boolean save() {
        try {
            AtomicFiles.writeProperties(file, properties, "PandaEye transport state - do not edit");
            return true;
        } catch (IOException e) {
            log.warn("could not write " + file.getFileName() + ": " + e);
            return false;
        }
    }
}
