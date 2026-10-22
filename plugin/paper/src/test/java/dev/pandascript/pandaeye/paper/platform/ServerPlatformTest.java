package dev.pandascript.pandaeye.paper.platform;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertTrue;

class ServerPlatformTest {

    @Test
    void versionsCompareNumerically() {
        assertTrue(ServerPlatform.compareVersions("1.21.11", "1.21.4") > 0);
        assertTrue(ServerPlatform.compareVersions("1.21.4", "1.21.4") == 0);
        assertTrue(ServerPlatform.compareVersions("1.21", "1.21.4") < 0);
        assertTrue(ServerPlatform.compareVersions("1.20.6", "1.21.4") < 0);
        assertTrue(ServerPlatform.compareVersions("1.22", "1.21.4") > 0);
        assertTrue(ServerPlatform.compareVersions("1.21.4-pre1", "1.21.4") >= 0);
    }
}
