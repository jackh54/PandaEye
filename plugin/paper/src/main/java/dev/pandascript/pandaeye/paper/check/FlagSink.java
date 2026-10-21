package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.Move;

import java.util.List;

/** Receives confirmed violations (VL, flag log, alerts, LocalFlag event, CheckStats, setback). Thread-safe. */
public interface FlagSink {

    /**
     * @param vl             the check's VL after adding the violation
     * @param worldConfirmed true when the confirmation step ran and agreed
     */
    void confirmed(CheckHandle handle, Violation violation, double vl, List<Move> context, boolean worldConfirmed,
                   long nanos);
}
