package dev.pandascript.pandaeye.paper.verdict;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.verdict.CircuitBreaker;
import dev.pandascript.pandaeye.common.verdict.ExecutedStore;
import dev.pandascript.pandaeye.common.verdict.VerdictProcessor;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.log.LogWriter;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import org.bukkit.command.Command;

import java.nio.file.Path;
import java.util.List;
import java.util.function.Function;
import java.util.function.Supplier;

/**
 * Verdict handling on Paper/Folia (ARCHITECTURE 4.7): the shared {@link VerdictProcessor} (de-duplication via
 * {@code executed.json}, the not-enforced reasons and their ack notes, placeholders, circuit breaker) on top of
 * {@link PaperVerdictPlatform}, plus the mitigation listeners' {@link MitigationController} and the enable-time
 * punishment command check. Verdicts arrive on the cloud thread.
 */
public final class VerdictHandler {

    private final Supplier<PluginConfig> config;
    private final MitigationController mitigations;
    private final AlertService alerts;
    private final Log log;
    private final VerdictProcessor processor;
    private volatile List<String> commandWarnings = List.of();

    public VerdictHandler(Supplier<PluginConfig> config, Enforcement enforcement, PlayerLookup players,
                          MitigationController mitigations, AlertService alerts, LogWriter logs, Log log, Path dataDir,
                          VerdictProcessor.Acknowledger acks) {
        this.config = config;
        this.mitigations = mitigations;
        this.alerts = alerts;
        this.log = log;
        ExecutedStore store = ExecutedStore.load(dataDir.resolve(ExecutedStore.FILE_NAME), log, System.currentTimeMillis());
        PaperVerdictPlatform platform = new PaperVerdictPlatform(config, enforcement, players, mitigations, alerts, logs);
        this.processor = new VerdictProcessor(platform, store, acks, log);
    }

    /** Cloud thread. */
    public void handle(Verdict verdict) {
        processor.handle(verdict);
    }

    /** Cloud thread: new tenant settings may switch enforcement (and so the mitigation listeners) on or off. */
    public void remoteSettingsChanged() {
        mitigations.refresh();
    }

    public MitigationController mitigations() {
        return mitigations;
    }

    public CircuitBreaker.Status breaker() {
        return processor.breakerStatus();
    }

    public void resetBreaker(String by) {
        processor.resetBreaker(by);
        alerts.notice("breaker", "punishment circuit breaker reset by " + by, System.nanoTime());
    }

    public ExecutedStore store() {
        return processor.store();
    }

    /**
     * Global region thread, first tick after enable (every plugin has registered its commands by then):
     * checks the ban and unban commands and warns loudly about problems.
     */
    public List<String> checkCommands(Function<String, Command> lookup) {
        List<String> warnings = CommandValidator.validate(PaperVerdictPlatform.punishments(config.get().punishments()),
                lookup);
        commandWarnings = List.copyOf(warnings);
        String suffix = config.get().shadow() ? " (shadow mode: nothing is executed until mode: live)" : "";
        for (String warning : warnings) {
            log.warn("!!! " + warning + suffix + " !!!");
        }
        return warnings;
    }

    public List<String> commandWarnings() {
        return commandWarnings;
    }
}
