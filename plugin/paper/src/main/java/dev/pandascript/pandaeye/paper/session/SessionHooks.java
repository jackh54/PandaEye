package dev.pandascript.pandaeye.paper.session;

import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.SessionEvents;

/** Non-telemetry reactions to packet moments: the setback target and provisional-exemption validation. */
public final class SessionHooks implements SessionEvents {

    private final Session session;
    private final ProvisionalValidator provisional;

    public SessionHooks(Session session, ProvisionalValidator provisional) {
        this.session = session;
        this.provisional = provisional;
    }

    @Override
    public void legitPosition(long nanos, Move move) {
        session.setbackTarget(new SetbackTarget(move.x(), move.y(), move.z(), move.yaw(), move.pitch(),
                session.signals().worldUid()));
    }

    @Override
    public void provisionalExemption(long nanos, boolean riptide) {
        provisional.validate(session, riptide);
    }
}
