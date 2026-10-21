package dev.pandascript.pandaeye.paper.session;

import java.util.concurrent.ConcurrentHashMap;

/** Entity id -> session for tracked players (filled when a session is seeded). Any thread. */
public final class EntityIndex {

    private final ConcurrentHashMap<Integer, Session> byEntityId = new ConcurrentHashMap<>();

    public void put(int entityId, Session session) {
        if (entityId >= 0) {
            byEntityId.put(entityId, session);
        }
    }

    public Session get(int entityId) {
        return entityId < 0 ? null : byEntityId.get(entityId);
    }

    public void remove(int entityId, Session session) {
        byEntityId.remove(entityId, session);
    }

    /** Removes every entry of the session (its id may be unknown to the caller). */
    public void removeAll(Session session) {
        byEntityId.values().remove(session);
    }

    public void clear() {
        byEntityId.clear();
    }
}
