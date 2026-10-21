package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.MoveHistory;
import dev.pandascript.pandaeye.paper.state.PacketState;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.function.Supplier;

/**
 * One check of one player: the check instance with its VL, tuning statistics and settings. Implements the
 * {@link CheckContext} the check sees on the netty thread; the confirmation step calls
 * {@link #confirmedByWorld} / {@link #discardedByWorld} from the entity thread.
 */
public final class CheckHandle implements CheckContext {

    private final Check check;
    private final Supplier<CheckSettings> settings;
    private final ViolationLevel vl;
    private final CheckStatsWindow stats = new CheckStatsWindow();
    private final PacketState state;
    private final FlagSink flags;
    private final CandidateSink candidates;
    private long nowNanos;
    private boolean raised;

    public CheckHandle(Check check, Supplier<CheckSettings> settings, PacketState state, FlagSink flags,
                       CandidateSink candidates) {
        this.check = check;
        this.settings = settings;
        this.vl = new ViolationLevel(check.descriptor().vlDecaySeconds());
        this.state = state;
        this.flags = flags;
        this.candidates = candidates;
    }

    public CheckDescriptor descriptor() {
        return check.descriptor();
    }

    public Check check() {
        return check;
    }

    public CheckStatsWindow stats() {
        return stats;
    }

    public ViolationLevel violationLevel() {
        return vl;
    }

    /** Runs the check for one input if enabled; returns true when it raised a flag or candidate. */
    boolean handle(CheckInput input, long nanos) {
        if (!settings.get().enabled()) {
            return false;
        }
        nowNanos = nanos;
        raised = false;
        check.handle(input, this);
        return raised;
    }

    @Override
    public PacketState state() {
        return state;
    }

    @Override
    public CheckSettings settings() {
        return settings.get();
    }

    @Override
    public long nowNanos() {
        return nowNanos;
    }

    @Override
    public double vl() {
        return vl.value(nowNanos);
    }

    @Override
    public void evaluated(double statistic) {
        stats.evaluated(statistic);
    }

    @Override
    public void flag(Violation violation) {
        raised = true;
        stats.candidate();
        stats.confirmed();
        double level = vl.add(violation.vlIncrement(), nowNanos);
        flags.confirmed(this, violation, level, context(), false, nowNanos);
    }

    @Override
    public boolean candidate(Violation violation, List<ConfirmContext.Position> positions, boolean movement,
                             Confirmer confirmer) {
        raised = true;
        stats.candidate();
        UUID world = state.signals().worldUid();
        if (world == null) {
            stats.discarded();
            return false;
        }
        return candidates.submit(this, new Candidate(violation, positions, movement, confirmer, world, state.epoch(),
                context(), nowNanos));
    }

    /** Entity thread: the confirmation step agreed. */
    public void confirmedByWorld(Candidate candidate, Violation violation, long nanos) {
        stats.confirmed();
        double level = vl.add(violation.vlIncrement(), nanos);
        flags.confirmed(this, violation, level, candidate.context(), true, candidate.nanos());
    }

    /** Entity thread: the confirmation step rejected the candidate. */
    public void discardedByWorld() {
        stats.discarded();
    }

    private List<Move> context() {
        MoveHistory history = state.moves();
        List<Move> moves = new ArrayList<>(history.size());
        for (int i = history.size() - 1; i >= 0; i--) {
            moves.add(history.get(i));
        }
        return moves;
    }
}
