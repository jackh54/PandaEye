package dev.pandascript.pandaeye.paper.state;

import dev.pandascript.pandaeye.paper.check.CheckInput;

import java.util.List;

/** Forwards every {@link SessionEvents} call to several receivers in order. */
public final class CompositeEvents implements SessionEvents {

    private final List<SessionEvents> receivers;

    public CompositeEvents(List<SessionEvents> receivers) {
        this.receivers = List.copyOf(receivers);
    }

    @Override
    public void joined(long nanos) {
        receivers.forEach(r -> r.joined(nanos));
    }

    @Override
    public void stateChanged(long nanos) {
        receivers.forEach(r -> r.stateChanged(nanos));
    }

    @Override
    public void move(long nanos, Move move) {
        for (SessionEvents receiver : receivers) {
            receiver.move(nanos, move);
        }
    }

    @Override
    public void legitPosition(long nanos, Move move) {
        for (SessionEvents receiver : receivers) {
            receiver.legitPosition(nanos, move);
        }
    }

    @Override
    public void attack(long nanos, int targetEntityId, boolean stab) {
        receivers.forEach(r -> r.attack(nanos, targetEntityId, stab));
    }

    @Override
    public void swing(long nanos, int hand) {
        receivers.forEach(r -> r.swing(nanos, hand));
    }

    @Override
    public void place(long nanos, int x, int y, int z, int face, float cursorX, float cursorY, float cursorZ, int hand,
                      boolean insideBlock) {
        receivers.forEach(r -> r.place(nanos, x, y, z, face, cursorX, cursorY, cursorZ, hand, insideBlock));
    }

    @Override
    public void dig(long nanos, CheckInput.DigAction action, int x, int y, int z, int face, PacketState.DigStart start) {
        receivers.forEach(r -> r.dig(nanos, action, x, y, z, face, start));
    }

    @Override
    public void click(long nanos, int windowId, int slot, int button, int mode) {
        receivers.forEach(r -> r.click(nanos, windowId, slot, button, mode));
    }

    @Override
    public void useItem(long nanos, int hand) {
        receivers.forEach(r -> r.useItem(nanos, hand));
    }

    @Override
    public void velocity(long nanos, VelocityTracker.Source source, double vx, double vy, double vz) {
        receivers.forEach(r -> r.velocity(nanos, source, vx, vy, vz));
    }

    @Override
    public void latency(long nanos, boolean pong, AckTracker.Reply reply) {
        receivers.forEach(r -> r.latency(nanos, pong, reply));
    }

    @Override
    public void teleport(long nanos, TeleportTracker.Pending teleport, boolean confirmed) {
        receivers.forEach(r -> r.teleport(nanos, teleport, confirmed));
    }

    @Override
    public void brand(long nanos, String brand) {
        receivers.forEach(r -> r.brand(nanos, brand));
    }

    @Override
    public void provisionalExemption(long nanos, boolean riptide) {
        receivers.forEach(r -> r.provisionalExemption(nanos, riptide));
    }
}
