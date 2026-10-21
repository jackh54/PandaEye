package dev.pandascript.pandaeye.paper.platform;

import dev.pandascript.pandaeye.protocol.v1.Platform;

/** Detects Folia (and forks such as Canvas) versus Paper. */
public final class ServerPlatform {

    private ServerPlatform() {
    }

    public static boolean folia() {
        try {
            Class.forName("io.papermc.paper.threadedregions.RegionizedServer");
            return true;
        } catch (ClassNotFoundException e) {
            return false;
        }
    }

    public static Platform platform() {
        return folia() ? Platform.PLATFORM_FOLIA : Platform.PLATFORM_PAPER;
    }
}
