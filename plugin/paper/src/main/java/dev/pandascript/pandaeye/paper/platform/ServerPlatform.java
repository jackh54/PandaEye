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

    /** The oldest Minecraft version PandaEye runs on: the Paper API it is compiled against. */
    public static final String MIN_MINECRAFT = "1.21.4";

    /**
     * Why this server cannot run PandaEye, or null when it can: Paper's region/entity schedulers (Paper, Folia
     * and their forks; not Spigot or CraftBukkit) and Minecraft {@link #MIN_MINECRAFT} or newer.
     */
    public static String unsupportedReason() {
        try {
            Class.forName("io.papermc.paper.threadedregions.scheduler.EntityScheduler");
        } catch (ClassNotFoundException e) {
            return "PandaEye needs Paper, Folia or a fork of them (Purpur, Pufferfish, ...); Spigot and CraftBukkit"
                    + " are not supported. PandaEye stays disabled.";
        }
        String version = org.bukkit.Bukkit.getMinecraftVersion();
        if (compareVersions(version, MIN_MINECRAFT) < 0) {
            return "PandaEye needs Minecraft " + MIN_MINECRAFT + " or newer (this server runs " + version
                    + "). Older client versions can still join through ViaVersion. PandaEye stays disabled.";
        }
        return null;
    }

    /** Compares dotted versions numerically ("1.21.11" > "1.21.4"); missing or non-numeric parts count as 0. */
    static int compareVersions(String a, String b) {
        String[] x = a.split("[.\\-]");
        String[] y = b.split("[.\\-]");
        for (int i = 0; i < Math.max(x.length, y.length); i++) {
            int c = Integer.compare(part(x, i), part(y, i));
            if (c != 0) {
                return c;
            }
        }
        return 0;
    }

    private static int part(String[] parts, int i) {
        if (i >= parts.length) {
            return 0;
        }
        try {
            return Integer.parseInt(parts[i].trim());
        } catch (NumberFormatException e) {
            return 0;
        }
    }
}
