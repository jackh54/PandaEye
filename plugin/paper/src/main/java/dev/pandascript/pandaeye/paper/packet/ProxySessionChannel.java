package dev.pandascript.pandaeye.paper.packet;

import dev.pandascript.pandaeye.common.proxy.ProxySessionToken;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.Session;

import java.util.Iterator;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Supplier;

/**
 * The {@code pandaeye:session} message from a PandaEye proxy (ARCHITECTURE 4.3), read at packet level and only
 * when {@code proxy.session-channel} is on. A message that arrives before the session exists (configuration
 * phase) is buffered by player UUID for 10 s. Accepted at most once per server connect. Accepted and rejected
 * messages (by reason) are counted for /pandaeye status.
 */
final class ProxySessionChannel {

    static final long BUFFER_NANOS = 10_000_000_000L;
    private static final int MAX_BUFFERED = 10_000;

    private record Buffered(byte[] data, long nanos) {
    }

    private final Supplier<PluginConfig> config;
    private final ProxySessionListener listener;
    private final ProxySessionCounters counters;
    private final ConcurrentHashMap<UUID, Buffered> early = new ConcurrentHashMap<>();

    ProxySessionChannel(Supplier<PluginConfig> config, ProxySessionListener listener, ProxySessionCounters counters) {
        this.config = config;
        this.listener = listener;
        this.counters = counters;
    }

    /** A message on the channel; {@code session} is null when it does not exist yet. */
    void received(UUID player, Session session, byte[] data, long nanos) {
        if (!config.get().proxy().sessionChannel() || player == null) {
            return;
        }
        if (session != null) {
            accept(session, data, nanos);
            return;
        }
        if (early.size() >= MAX_BUFFERED) {
            expire(nanos);
        }
        if (early.size() < MAX_BUFFERED) {
            early.put(player, new Buffered(data, nanos));
        }
    }

    /** A session was created: applies a message buffered for its player in the last 10 s. */
    void sessionStarted(Session session, long nanos) {
        Buffered buffered = early.remove(session.uuid());
        if (buffered != null && nanos - buffered.nanos() <= BUFFER_NANOS && config.get().proxy().sessionChannel()) {
            accept(session, buffered.data(), nanos);
        }
    }

    private void accept(Session session, byte[] data, long nanos) {
        ProxySessionToken.verify(data, config.get().proxy().secret(), session.uuid(), System.currentTimeMillis() / 1_000,
                        counters::count)
                .filter(verified -> session.acceptProxySession(verified.networkSession().toString()))
                .ifPresent(verified -> {
                    counters.countAccepted();
                    listener.accepted(session, nanos);
                });
    }

    private void expire(long nanos) {
        Iterator<Map.Entry<UUID, Buffered>> it = early.entrySet().iterator();
        while (it.hasNext()) {
            if (nanos - it.next().getValue().nanos() > BUFFER_NANOS) {
                it.remove();
            }
        }
    }
}
