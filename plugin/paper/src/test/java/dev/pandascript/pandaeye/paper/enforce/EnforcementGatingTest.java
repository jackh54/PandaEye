package dev.pandascript.pandaeye.paper.enforce;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.CloudClient;
import dev.pandascript.pandaeye.common.transport.CloudConfig;
import dev.pandascript.pandaeye.common.transport.CloudListener;
import dev.pandascript.pandaeye.common.transport.CloudState;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.protocol.v1.Platform;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.net.URI;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Enforcement needs local mode live AND the last-known RemoteConfig.enforcement (persisted in
 * remote-config.properties, false until a Welcome has ever said otherwise) AND the feature switch
 * (ARCHITECTURE 1.1). Uses the real transport against an unreachable cloud, so only the persisted value counts.
 */
class EnforcementGatingTest {

    @TempDir
    Path dir;

    private final Log log = new Log() {
        @Override
        public void info(String message) {
        }

        @Override
        public void warn(String message) {
        }

        @Override
        public void warn(String message, Throwable error) {
        }

        @Override
        public void debug(String message) {
        }
    };

    private final ShadowEnforcer shadow = new ShadowEnforcer();
    private final LiveEnforcer live = new LiveEnforcer(null, task -> {
    });

    @Test
    void theMatrixOfModeCloudSwitchAndFeatureSwitch() {
        for (boolean liveMode : new boolean[]{false, true}) {
            for (boolean cloud : new boolean[]{false, true}) {
                RemoteSettings remote = new RemoteSettings(4, true, true, true, false, cloud, 1);
                Enforcement enforcement = new Enforcement(() -> liveMode, () -> remote, shadow, live);
                for (boolean feature : new boolean[]{false, true}) {
                    Enforcer expected = liveMode && cloud && feature ? live : shadow;
                    assertSame(expected, enforcement.forFeature(feature), liveMode + " " + cloud + " " + feature);
                }
                assertSame(live, enforcement.correctiveUnban(), "the UNBAN exception");
            }
        }
        Enforcement unknown = new Enforcement(() -> true, () -> RemoteSettings.UNKNOWN, shadow, live);
        assertFalse(unknown.enabled(), "enforcement is off until the cloud has ever said otherwise");
    }

    private CloudClient client() {
        CloudConfig config = new CloudConfig(URI.create("ws://127.0.0.1:9/v1/connect"), "test-license", "test", "",
                Platform.PLATFORM_PAPER, "test", "0.0.0", "1.21.4", false, CloudClient.CAPABILITIES,
                Duration.ofMillis(20), 1 << 20, Duration.ofMinutes(1), dir);
        return new CloudClient(config, new CloudListener() {
        }, log, EventClock.system(), () -> 0);
    }

    private static void await(BooleanSupplier condition) throws InterruptedException {
        long deadline = System.nanoTime() + Duration.ofSeconds(10).toNanos();
        while (!condition.getAsBoolean()) {
            if (System.nanoTime() > deadline) {
                throw new AssertionError("timed out");
            }
            Thread.sleep(5);
        }
    }

    private void persist(String enforcement, long receivedUnixMs) throws Exception {
        Files.writeString(dir.resolve("remote-config.properties"), "leniency=4\nautoban=true\nautokick=true\n"
                + "mitigations=true\nreputation-on-join=false\nenforcement=" + enforcement + "\nreceived-unix-ms="
                + receivedUnixMs + "\n");
    }

    @Test
    void aPersistedEnforcementTrueEnablesEnforcementInLiveModeOnly() throws Exception {
        persist("true", 1_700_000_000_000L);
        CloudClient cloud = client();
        AtomicBoolean liveMode = new AtomicBoolean(true);
        Enforcement enforcement = new Enforcement(liveMode::get, cloud::remoteSettings, shadow, live);
        assertFalse(enforcement.enabled(), "nothing is known before the transport has loaded its files");
        try {
            cloud.start();
            await(() -> cloud.remoteSettings().known());
            assertTrue(enforcement.cloudEnforcement());
            assertTrue(enforcement.enabled());
            assertSame(live, enforcement.forFeature(true));
            assertSame(shadow, enforcement.forFeature(false), "the feature's own switch");
            liveMode.set(false);
            assertFalse(enforcement.enabled(), "shadow mode wins over the cloud");
            assertSame(shadow, enforcement.forFeature(true));
        } finally {
            cloud.stop(Duration.ofMillis(200));
        }
    }

    @Test
    void aPersistedEnforcementFalseOrNoFileKeepsEnforcementOff() throws Exception {
        persist("false", 1_700_000_000_000L);
        CloudClient cloud = client();
        Enforcement enforcement = new Enforcement(() -> true, cloud::remoteSettings, shadow, live);
        try {
            cloud.start();
            await(() -> cloud.remoteSettings().known());
            assertFalse(enforcement.enabled());
        } finally {
            cloud.stop(Duration.ofMillis(200));
        }
        Files.delete(dir.resolve("remote-config.properties"));
        CloudClient fresh = client();
        Enforcement unknown = new Enforcement(() -> true, fresh::remoteSettings, shadow, live);
        try {
            fresh.start();
            await(() -> fresh.state() != CloudState.STARTING);
            assertFalse(fresh.remoteSettings().known());
            assertFalse(unknown.enabled());
        } finally {
            fresh.stop(Duration.ofMillis(200));
        }
    }

    @Test
    void aCorruptOrNeverReceivedFileKeepsEnforcementOff() throws Exception {
        Files.writeString(dir.resolve("remote-config.properties"), "enforcement=true\nreceived-unix-ms=garbage\n");
        CloudClient cloud = client();
        Enforcement enforcement = new Enforcement(() -> true, cloud::remoteSettings, shadow, live);
        try {
            cloud.start();
            await(() -> cloud.state() != CloudState.STARTING);
            assertFalse(enforcement.enabled());
        } finally {
            cloud.stop(Duration.ofMillis(200));
        }
        persist("true", 0);
        CloudClient never = client();
        Enforcement neverReceived = new Enforcement(() -> true, never::remoteSettings, shadow, live);
        try {
            never.start();
            await(() -> never.state() != CloudState.STARTING);
            assertFalse(neverReceived.enabled(), "received-unix-ms 0 means a Welcome never said so");
        } finally {
            never.stop(Duration.ofMillis(200));
        }
    }
}
