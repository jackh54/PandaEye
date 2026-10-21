package dev.pandascript.pandaeye.paper.flag;

import dev.pandascript.pandaeye.paper.check.CheckDescriptor;
import dev.pandascript.pandaeye.paper.check.FlagSink;
import dev.pandascript.pandaeye.paper.check.Violation;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.log.LogWriter;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;

import java.util.List;
import java.util.concurrent.atomic.LongAdder;
import java.util.function.Supplier;

/**
 * What happens to a confirmed violation (ARCHITECTURE 4.5): telemetry focus, LocalFlag event with movement
 * context, flag log, alerts, and a setback in live mode. Thread-safe (netty and entity threads).
 */
public final class FlagPipeline {

    private final Telemetry telemetry;
    private final LogWriter logs;
    private final AlertService alerts;
    private final SetbackService setbacks;
    private final Supplier<PluginConfig> config;
    private final LongAdder confirmed = new LongAdder();

    public FlagPipeline(Telemetry telemetry, LogWriter logs, AlertService alerts, SetbackService setbacks,
                        Supplier<PluginConfig> config) {
        this.telemetry = telemetry;
        this.logs = logs;
        this.alerts = alerts;
        this.setbacks = setbacks;
        this.config = config;
    }

    public FlagSink forSession(Session session) {
        return (handle, violation, vl, context, worldConfirmed, nanos) ->
                confirmed(session, handle.descriptor(), violation, vl, context, worldConfirmed, nanos);
    }

    public long confirmedTotal() {
        return confirmed.sum();
    }

    private void confirmed(Session session, CheckDescriptor check, Violation violation, double vl, List<Move> context,
                           boolean worldConfirmed, long nanos) {
        confirmed.increment();
        long now = System.nanoTime();
        session.flagged(now);
        telemetry.localFlag(session, check, violation, vl, context, worldConfirmed, nanos);
        if (config.get().alerts().logFile()) {
            logs.flag(new LogWriter.FlagRecord(session.uuid(), session.name(), check.id(), violation.severity(), vl,
                    violation.info(), System.currentTimeMillis(), now));
        }
        alerts.flag(session.uuid(), session.name(), check.id(), vl, violation.info(), now);
        setbacks.consider(session, check, vl);
    }
}
