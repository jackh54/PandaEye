package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.protocol.player.User;

import java.util.UUID;

/** Public access to {@link TestPacketEvents} for tests in other packages: a PLAY-state User with the test API installed. */
public final class TestUsers {

    private TestUsers() {
    }

    public static User user(UUID uuid) {
        TestPacketEvents.install();
        return TestPacketEvents.user(uuid);
    }
}
