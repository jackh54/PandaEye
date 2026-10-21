package dev.pandascript.pandaeye.paper.state;

/** The last {@value #SIZE} movement packets of one player (LocalFlag context and n-1/n-2 access). Netty thread only. */
public final class MoveHistory {

    public static final int SIZE = 40;

    private final Move[] ring = new Move[SIZE];
    private int count;
    private int next;

    void add(Move move) {
        ring[next] = move;
        next = (next + 1) % SIZE;
        count = Math.min(SIZE, count + 1);
    }

    void clear() {
        java.util.Arrays.fill(ring, null);
        count = 0;
        next = 0;
    }

    public int size() {
        return count;
    }

    /** {@code get(0)} is the newest packet, {@code get(1)} the one before; null beyond the history. */
    public Move get(int back) {
        if (back < 0 || back >= count) {
            return null;
        }
        return ring[(next - 1 - back + SIZE) % SIZE];
    }

    /** True when packets 0..depth-1 are each consecutive with the packet before them and all carry a position. */
    public boolean consecutivePositions(int depth) {
        if (count < depth + 1) {
            return false;
        }
        for (int i = 0; i < depth; i++) {
            Move move = get(i);
            if (!move.consecutive() || !move.hasPos() || move.teleport()) {
                return false;
            }
        }
        Move oldest = get(depth);
        return oldest.hasPos();
    }
}
