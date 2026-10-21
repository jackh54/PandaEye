package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.AtomicFiles;

import java.io.IOException;
import java.nio.file.Path;
import java.util.Properties;

/** {@code remote-config.properties}: the last-known {@link RemoteSettings} and when they were received. */
final class RemoteSettingsStore {

    private final Path file;
    private final Log log;

    RemoteSettingsStore(Path file, Log log) {
        this.file = file;
        this.log = log;
    }

    RemoteSettings load() {
        try {
            Properties p = AtomicFiles.readProperties(file);
            long received = Long.parseLong(p.getProperty("received-unix-ms", "0").trim());
            if (received <= 0) {
                return RemoteSettings.UNKNOWN;
            }
            return new RemoteSettings(
                    Integer.parseInt(p.getProperty("leniency", "4").trim()),
                    Boolean.parseBoolean(p.getProperty("autoban")),
                    Boolean.parseBoolean(p.getProperty("autokick")),
                    Boolean.parseBoolean(p.getProperty("mitigations")),
                    Boolean.parseBoolean(p.getProperty("reputation-on-join")),
                    Boolean.parseBoolean(p.getProperty("enforcement")),
                    received);
        } catch (IOException | RuntimeException e) {
            log.warn("could not read " + file.getFileName() + ", enforcement stays off until the cloud answers: " + e);
            return RemoteSettings.UNKNOWN;
        }
    }

    void save(RemoteSettings settings) {
        Properties p = new Properties();
        p.setProperty("leniency", Integer.toString(settings.leniency()));
        p.setProperty("autoban", Boolean.toString(settings.autoban()));
        p.setProperty("autokick", Boolean.toString(settings.autokick()));
        p.setProperty("mitigations", Boolean.toString(settings.mitigations()));
        p.setProperty("reputation-on-join", Boolean.toString(settings.reputationOnJoin()));
        p.setProperty("enforcement", Boolean.toString(settings.enforcement()));
        p.setProperty("received-unix-ms", Long.toString(settings.receivedUnixMs()));
        try {
            AtomicFiles.writeProperties(file, p, "Last settings received from the PandaEye cloud");
        } catch (IOException e) {
            log.warn("could not write " + file.getFileName() + ": " + e);
        }
    }
}
