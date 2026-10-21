package dev.pandascript.pandaeye.paper.packet;

import dev.pandascript.pandaeye.paper.session.Session;

/** Called on the netty thread once a verified {@code pandaeye:session} message was accepted for a session. */
@FunctionalInterface
public interface ProxySessionListener {

    void accepted(Session session, long nanos);
}
