package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayDeque;

/**
 * Mirrors the client's {@code carriedIndex} (CHECKS 1.9): 0 on JOIN_GAME, changed only by the client's
 * HELD_ITEM_CHANGE. A server SET_HELD_ITEM to a different slot expects exactly one echo. Netty thread only.
 */
public final class SlotTracker {

    public enum Change {
        /** A normal slot change. */
        CHANGED,
        /** The echo of a server SET_HELD_ITEM. */
        ECHO,
        /** A change to the slot that was already selected. */
        REDUNDANT,
        /** Slot outside 0..8. */
        OUT_OF_RANGE,
        /** The mirror is unknown (hot load without baseline). */
        UNKNOWN
    }

    private static final int MAX_EXPECTED = 8;

    private final ArrayDeque<Integer> expectedEchoes = new ArrayDeque<>();
    private int mirror = -1;

    void reset(int slot) {
        mirror = slot;
        expectedEchoes.clear();
    }

    void serverSet(int slot) {
        if (mirror >= 0 && slot != mirror && expectedEchoes.size() < MAX_EXPECTED) {
            expectedEchoes.addLast(slot);
        }
    }

    Change clientSet(int slot) {
        if (slot < 0 || slot > 8) {
            return Change.OUT_OF_RANGE;
        }
        if (mirror < 0) {
            mirror = slot;
            return Change.UNKNOWN;
        }
        if (!expectedEchoes.isEmpty() && expectedEchoes.peekFirst() == slot) {
            expectedEchoes.pollFirst();
            mirror = slot;
            return Change.ECHO;
        }
        Change change = slot == mirror ? Change.REDUNDANT : Change.CHANGED;
        mirror = slot;
        return change;
    }

    /** The mirrored slot, -1 when unknown. */
    public int mirror() {
        return mirror;
    }

    public int expectedEchoes() {
        return expectedEchoes.size();
    }
}
