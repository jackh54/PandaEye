package dev.pandascript.pandaeye.paper.state;

import java.util.ArrayDeque;
import java.util.Iterator;

/**
 * Passive client acknowledgement (CHECKS 1.1): remembers every server KEEP_ALIVE and PING (sent by anyone)
 * and matches the client's replies. {@link #ackedUpTo()} is the send time of the newest server packet the
 * client has provably processed. Entries expire after 30 s; a keep-alive id sent twice within 30 s is
 * marked as plugin-injected (its RTT is not a clean sample). Netty thread only.
 */
public final class AckTracker {

    static final long EXPIRY_NANOS = 30_000_000_000L;
    private static final int MAX_ENTRIES = 512;

    public record Reply(boolean matched, long rttNanos, boolean injected) {

        static final Reply UNMATCHED = new Reply(false, 0, false);
    }

    private static final class Sent {
        final boolean ping;
        final long id;
        final long nanos;
        boolean injected;

        Sent(boolean ping, long id, long nanos) {
            this.ping = ping;
            this.id = id;
            this.nanos = nanos;
        }
    }

    private final ArrayDeque<Sent> sent = new ArrayDeque<>();
    private long ackedUpTo;
    private boolean anyAck;

    void serverKeepAlive(long id, long nanos) {
        expire(nanos);
        Sent entry = new Sent(false, id, nanos);
        for (Sent previous : sent) {
            if (!previous.ping && previous.id == id) {
                previous.injected = true;
                entry.injected = true;
            }
        }
        add(entry);
    }

    void serverPing(int id, long nanos) {
        expire(nanos);
        add(new Sent(true, id, nanos));
    }

    Reply clientKeepAlive(long id, long nanos) {
        return reply(false, id, nanos);
    }

    Reply clientPong(int id, long nanos) {
        return reply(true, id, nanos);
    }

    /** Send time of the newest server keep-alive/ping the client has answered; only valid if {@link #hasAck()}. */
    public long ackedUpTo() {
        return ackedUpTo;
    }

    public boolean hasAck() {
        return anyAck;
    }

    private Reply reply(boolean ping, long id, long nowNanos) {
        Sent match = null;
        for (Sent entry : sent) {
            if (entry.ping == ping && entry.id == id) {
                match = entry;
                break;
            }
        }
        if (match == null) {
            return Reply.UNMATCHED;
        }
        Iterator<Sent> it = sent.iterator();
        while (it.hasNext()) {
            Sent entry = it.next();
            if (entry.ping == ping) {
                it.remove();
            }
            if (entry == match) {
                break;
            }
        }
        if (!anyAck || match.nanos - ackedUpTo > 0) {
            ackedUpTo = match.nanos;
            anyAck = true;
        }
        return new Reply(true, nowNanos - match.nanos, match.injected);
    }

    private void add(Sent entry) {
        if (sent.size() == MAX_ENTRIES) {
            sent.pollFirst();
        }
        sent.addLast(entry);
    }

    private void expire(long nowNanos) {
        while (!sent.isEmpty() && nowNanos - sent.peekFirst().nanos > EXPIRY_NANOS) {
            sent.pollFirst();
        }
    }
}
