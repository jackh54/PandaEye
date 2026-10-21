package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.codec.FrameCodec;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.Error;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.Hello;
import dev.pandascript.pandaeye.protocol.v1.MoveSample;
import dev.pandascript.pandaeye.protocol.v1.Platform;
import dev.pandascript.pandaeye.protocol.v1.ProbeResult;
import dev.pandascript.pandaeye.protocol.v1.RemoteConfig;
import dev.pandascript.pandaeye.protocol.v1.ServerFrame;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.PosixFilePermissions;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeoutException;
import java.util.function.BooleanSupplier;
import java.util.random.RandomGenerator;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

class CloudClientTest {

    private static final Duration WAIT = Duration.ofSeconds(5);
    private static final CloudClient.Timings FAST = new CloudClient.Timings(Duration.ofSeconds(2),
            Duration.ofSeconds(2), Duration.ofMillis(200), 50, 200, 5_000, 3_000, Duration.ofMillis(200));

    @TempDir
    Path dataDir;

    private FakeGateway gateway;
    private final List<CloudClient> clients = new ArrayList<>();
    private final TestLog log = new TestLog();
    private final RecordingListener listener = new RecordingListener();

    @BeforeEach
    void startGateway() throws Exception {
        gateway = new FakeGateway();
    }

    @AfterEach
    void cleanUp() throws Exception {
        for (CloudClient client : clients) {
            client.stop(Duration.ofMillis(300));
        }
        gateway.close();
    }

    @Test
    void sendsHelloThenStreamsAcknowledgedBatches() throws Exception {
        CloudClient client = start();
        Hello hello = gateway.await(ClientFrame::hasHello, WAIT).frame().getHello();
        assertEquals(1, hello.getProtocolVersion());
        assertEquals("license-1", hello.getLicenseKey());
        assertTrue(Uuids.isCanonical(hello.getServerId()));
        assertEquals(client.instanceId(), hello.getInstanceId());
        assertTrue(hello.getShadowMode());
        assertEquals(CloudClient.CAPABILITIES, hello.getCapabilitiesList());
        assertEquals(7, hello.getOnlinePlayers());
        awaitState(client, CloudState.READY);

        for (int i = 0; i < 3; i++) {
            client.sink().offer(move(i));
        }
        Batch batch = gateway.await(f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT).frame().getBatch();
        assertTrue(batch.getSeq() >= 1);
        assertFalse(batch.getReplayed());
        assertTrue(batch.getSentUnixMs() > 0);
        waitUntil(() -> client.status().pendingBatches() == 0, "batches acknowledged");
    }

    @Test
    void stopIsBoundedWhenTheCloudNeverAnswersTheCloseFrame() throws Exception {
        gateway.answerClose = false;
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        clients.remove(client);

        long started = System.nanoTime();
        client.stop(Duration.ofMillis(280));
        long elapsedMillis = (System.nanoTime() - started) / 1_000_000;

        assertTrue(elapsedMillis < 300, "stop took " + elapsedMillis + " ms");
        assertEquals(List.of(), pandaEyeThreads());
        waitUntil(() -> gateway.connection(0).closeReceived, "close frame sent");
        assertEquals("plugin stopping", gateway.await(ClientFrame::hasGoodbye, WAIT).frame().getGoodbye().getReason());
    }

    @Test
    void resendsUnacknowledgedBatchesWithTheirSeqAfterReconnectAndDropsAcknowledgedOnes() throws Exception {
        gateway.autoAck = false;
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        client.sink().offer(move(1));
        long first = gateway.await(f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT).frame().getBatch().getSeq();
        int mark = gateway.received().size() - 1;
        client.sink().offer(move(2));
        long second = gateway.awaitAfter(mark, f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT)
                .frame().getBatch().getSeq();
        assertEquals(first + 1, second);

        gateway.lastAckedSeq = first;
        gateway.connection(0).kill();
        gateway.awaitConnection(1, WAIT);
        FakeGateway.Received resent = gateway.await(f -> f.hasBatch() && f.getBatch().getReplayed(), WAIT);
        assertEquals(1, resent.connection());
        assertEquals(second, resent.frame().getBatch().getSeq());
        assertTrue(gateway.frames(f -> f.hasBatch() && f.getBatch().getSeq() == first).size() == 1,
                "the acknowledged batch is never resent");

        int mark2 = gateway.received().size() - 1;
        client.sink().offer(move(3));
        Batch live = gateway.awaitAfter(mark2, f -> f.hasBatch() && hasMoves(f.getBatch()) && !f.getBatch().getReplayed(),
                WAIT).frame().getBatch();
        assertTrue(live.getSeq() > second);
    }

    @Test
    void welcomeAboveTheLocalSeqMovesTheAllocator() throws Exception {
        gateway.lastAckedSeq = 5_000_000;
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        client.sink().offer(move(1));
        Batch batch = gateway.await(ClientFrame::hasBatch, WAIT).frame().getBatch();
        assertEquals(5_000_001, batch.getSeq());
    }

    @Test
    void sendsLiveBatchesFirstAndPacesReplayedOnesToHalfTheReplayBudget() throws Exception {
        gateway.acceptHandshakes = false;
        gateway.maxBatchEvents = 10;
        gateway.replayEventsPerSecond = 100;
        CloudClient client = start();
        for (int i = 0; i < 30; i++) {
            for (int j = 0; j < 10; j++) {
                client.sink().offer(move(i * 10 + j));
            }
            Thread.sleep(15);
        }
        waitUntil(() -> client.status().replayBatches() >= 10, "replay backlog built");
        gateway.acceptHandshakes = true;
        awaitState(client, CloudState.READY);
        long readyAt = System.nanoTime();
        client.sink().offer(move(9_999));
        FakeGateway.Received live = gateway.await(f -> f.hasBatch() && hasMoves(f.getBatch())
                && !f.getBatch().getReplayed(), WAIT);
        Thread.sleep(1_000);
        long windowEnd = readyAt + 1_000_000_000L;
        int replayedEvents = 0;
        for (FakeGateway.Received r : gateway.received()) {
            if (r.frame().hasBatch() && r.frame().getBatch().getReplayed() && r.nanos() <= windowEnd) {
                replayedEvents += r.frame().getBatch().getEventsCount();
            }
        }
        // 50 events/s with a bucket of 50 events: at most ~100 events (+ one batch of slack) in the first second.
        assertTrue(replayedEvents <= 110, "replayed " + replayedEvents + " events in the first second");
        assertTrue(replayedEvents >= 20, "replay must progress, got " + replayedEvents);
        assertTrue(live.nanos() - readyAt < 500_000_000L, "live data is not held back by the replay");
    }

    @Test
    void unauthenticatedWaitsLongAndKeepsTheBuffer() throws Exception {
        gateway.autoWelcome = false;
        gateway.script = frame -> frame.hasHello()
                ? List.of(error(Error.Code.CODE_UNAUTHENTICATED, 0))
                : List.of();
        CloudClient client = start();
        client.sink().offer(move(1));
        gateway.await(ClientFrame::hasHello, WAIT);
        waitUntil(() -> client.state() == CloudState.WAITING, "waiting after the error");
        Thread.sleep(800);
        assertEquals(1, gateway.connectionCount(), "no reconnect within the long wait");
        assertTrue(client.status().pendingBatches() >= 1, "buffer kept");
        assertTrue(log.contains("license sha256:"), log.dump());
        assertFalse(log.contains("license-1"), "the license key is never logged");
    }

    @Test
    void rateLimitedHonoursRetryAfter() throws Exception {
        gateway.autoWelcome = false;
        gateway.script = frame -> frame.hasHello() && gateway.connectionCount() == 1
                ? List.of(error(Error.Code.CODE_RATE_LIMITED, 700))
                : List.of();
        CloudClient client = start();
        gateway.await(ClientFrame::hasHello, WAIT);
        gateway.autoWelcome = true;
        gateway.awaitConnection(1, WAIT);
        awaitState(client, CloudState.READY);
        List<Long> hellos = gateway.received().stream().filter(r -> r.frame().hasHello())
                .map(FakeGateway.Received::nanos).toList();
        long gapMillis = (hellos.get(1) - hellos.get(0)) / 1_000_000;
        assertTrue(gapMillis >= 600, "reconnected after " + gapMillis + " ms");
    }

    @Test
    void duplicateServerWaitsAndExplainsHowToFixIt() throws Exception {
        gateway.autoWelcome = false;
        gateway.script = frame -> frame.hasHello() ? List.of(error(Error.Code.CODE_DUPLICATE_SERVER, 0)) : List.of();
        CloudClient client = start();
        gateway.await(ClientFrame::hasHello, WAIT);
        waitUntil(() -> client.state() == CloudState.WAITING, "waiting");
        Thread.sleep(500);
        assertEquals(1, gateway.connectionCount());
        assertTrue(log.contains("delete plugins/PandaEye/server-id"), log.dump());
    }

    @Test
    void tooLargeIsCountedAndNotFatal() throws Exception {
        gateway.script = frame -> frame.hasBatch()
                ? List.of(ServerFrame.newBuilder().setError(Error.newBuilder()
                .setCode(Error.Code.CODE_TOO_LARGE).setMessage("too large")).build())
                : List.of();
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        client.sink().offer(move(1));
        waitUntil(() -> client.status().tooLargeBatches() >= 1, "counted");
        assertEquals(CloudState.READY, client.state());
    }

    @Test
    void remoteConfigIsAppliedPersistedAndReloaded() throws Exception {
        gateway.heartbeatMs = 100;
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        assertFalse(client.remoteSettings().enforcement());
        gateway.remoteConfig = RemoteConfig.newBuilder().setLeniency(2).setEnforcement(true).build();
        waitUntil(() -> client.remoteSettings().enforcement(), "enforcement from Pong");
        assertEquals(2, client.remoteSettings().leniency());
        assertTrue(listener.settings.stream().anyMatch(RemoteSettings::enforcement));
        waitUntil(() -> client.status().connectionRttMicros() > 0, "connection RTT measured");
        client.stop(Duration.ofMillis(300));
        clients.remove(client);

        String stored = Files.readString(dataDir.resolve("remote-config.properties"));
        assertTrue(stored.contains("enforcement=true"), stored);
        gateway.acceptHandshakes = false;
        CloudClient restarted = start();
        waitUntil(() -> restarted.remoteSettings().enforcement(), "loaded before any Welcome");
    }

    @Test
    void spoolsUnacknowledgedBatchesOnStopAndResendsThemAsReplayedAfterRestart() throws Exception {
        gateway.autoAck = false;
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        client.sink().offer(move(42));
        long seq = gateway.await(f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT).frame().getBatch().getSeq();
        client.stop(Duration.ofMillis(300));
        clients.remove(client);
        assertTrue(Files.list(dataDir.resolve("spool")).findAny().isPresent(), "spool written");

        int mark = gateway.received().size() - 1;
        gateway.autoAck = true;
        CloudClient restarted = start();
        Batch resent = gateway.awaitAfter(mark, f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT).frame().getBatch();
        assertEquals(seq, resent.getSeq());
        assertTrue(resent.getReplayed());
        assertEquals(42, resent.getEvents(0).getTick());
        waitUntil(() -> restarted.status().pendingBatches() == 0, "acknowledged after restart");
        assertTrue(Files.list(dataDir.resolve("spool")).findAny().isEmpty(), "spool consumed");
    }

    @Test
    void undrainedEventsAreSpooledAndSentWithANewSeq() throws Exception {
        gateway.acceptHandshakes = false;
        CloudConfig slow = config(Duration.ofSeconds(30));
        CloudClient client = new CloudClient(slow, listener, log, EventClock.system(), () -> 0, FAST,
                new FrameCodec(log), RandomGenerator.getDefault());
        client.start();
        waitUntil(() -> Files.exists(dataDir.resolve("state.properties")), "initialised");
        client.sink().offer(move(7));
        client.stop(Duration.ofMillis(300));

        gateway.acceptHandshakes = true;
        start();
        Batch batch = gateway.await(f -> f.hasBatch() && hasMoves(f.getBatch()), WAIT).frame().getBatch();
        assertTrue(batch.getReplayed());
        assertTrue(batch.getSeq() > SeqAllocator.BLOCK, "a restart resumes above the reserved block");
    }

    @Test
    void verdictsReachTheListenerAndAcksReachTheCloud() throws Exception {
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        gateway.connection(0).send(ServerFrame.newBuilder().setVerdict(Verdict.newBuilder()
                .setVerdictId("v-1").setPlayer(Uuids.toByteString(UUID.randomUUID()))).build());
        waitUntil(() -> !listener.verdicts.isEmpty(), "verdict delivered");
        client.ackVerdict("v-1", false, "shadow");
        ClientFrame ack = gateway.await(ClientFrame::hasVerdictAck, WAIT).frame();
        assertEquals("v-1", ack.getVerdictAck().getVerdictId());
        assertEquals("shadow", ack.getVerdictAck().getNote());
    }

    @Test
    void fragmentedMessagesAreReassembled() throws Exception {
        gateway.autoWelcome = false;
        gateway.script = frame -> List.of();
        CloudClient client = start();
        gateway.await(ClientFrame::hasHello, WAIT);
        gateway.connection(0).sendText("ignored");
        gateway.connection(0).sendFragmented(ServerFrame.newBuilder().setWelcome(
                dev.pandascript.pandaeye.protocol.v1.Welcome.newBuilder().setTenantId("t_frag")
                        .setHeartbeatIntervalMs(5_000)).build(), 4);
        awaitState(client, CloudState.READY);
    }

    @Test
    void oversizedMessagesDropTheConnection() throws Exception {
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        gateway.connection(0).sendRaw(new byte[FrameCodec.MAX_COMPRESSED_BYTES + 10]);
        gateway.awaitConnection(1, WAIT);
        assertTrue(log.contains("exceeds"), log.dump());
        awaitState(client, CloudState.READY);
    }

    @Test
    void silenceTimesOutAndReconnects() throws Exception {
        gateway.heartbeatMs = 100;
        gateway.autoPong = false;
        gateway.autoAck = false;
        start();
        gateway.awaitConnection(1, WAIT);
        assertTrue(log.contains("no frame from the cloud"), log.dump());
    }

    @Test
    void probesTravelInBatchesAndTheirRttIsReported() throws Exception {
        gateway.heartbeatMs = 150;
        gateway.script = frame -> {
            List<ServerFrame> replies = new ArrayList<>();
            if (frame.hasBatch()) {
                for (Event event : frame.getBatch().getEventsList()) {
                    if (event.hasProbe()) {
                        replies.add(ServerFrame.newBuilder().setProbeResult(ProbeResult.newBuilder()
                                .setProbeId(event.getProbe().getProbeId()).setEventTimeUs(event.getTimeUs())).build());
                    }
                }
            }
            return replies;
        };
        CloudClient client = start();
        waitUntil(() -> !listener.probeRtts.isEmpty(), "probe result");
        waitUntil(() -> client.status().probeP95Micros() > 0, "probe stats");
        gateway.await(f -> f.hasPing() && f.getPing().getLastProbeRttUs() > 0, WAIT);
    }

    @Test
    void storesTheIssuedSecretOwnerOnlyAndPresentsItOnReconnect() throws Exception {
        gateway.serverSecret = "s3cret";
        CloudClient client = start();
        awaitState(client, CloudState.READY);
        Path secret = dataDir.resolve(ServerIdentity.SERVER_SECRET_FILE);
        waitUntil(() -> Files.exists(secret), "secret stored");
        assertEquals("s3cret", Files.readString(secret).trim());
        assertEquals("rw-------", PosixFilePermissions.toString(Files.getPosixFilePermissions(secret)));
        assertEquals("rw-------", PosixFilePermissions.toString(
                Files.getPosixFilePermissions(dataDir.resolve(ServerIdentity.SERVER_ID_FILE))));
        gateway.serverSecret = "";
        gateway.connection(0).kill();
        Hello again = gateway.awaitAfter(0, ClientFrame::hasHello, WAIT).frame().getHello();
        assertEquals("s3cret", again.getServerSecret());
    }

    // ---------------------------------------------------------------------------------------------

    private CloudClient start() {
        CloudClient client = new CloudClient(config(Duration.ofMillis(10)), listener, log, EventClock.system(), () -> 7,
                FAST, new FrameCodec(log), RandomGenerator.getDefault());
        clients.add(client);
        client.start();
        return client;
    }

    private CloudConfig config(Duration batchInterval) {
        return new CloudConfig(gateway.uri(), "license-1", "Test", "", Platform.PLATFORM_PAPER, "test",
                "0.0.0", "1.21.11", true, CloudClient.CAPABILITIES, batchInterval, 64L << 20,
                Duration.ofMinutes(10), dataDir);
    }

    private static Event move(int tick) {
        return Event.newBuilder().setPlayer(Uuids.toByteString(new UUID(1, 2))).setTimeUs(1).setTick(tick)
                .setMove(MoveSample.newBuilder().setX(tick).setHasPos(true)).build();
    }

    private static boolean hasMoves(Batch batch) {
        return batch.getEventsList().stream().anyMatch(Event::hasMove);
    }

    private static ServerFrame error(Error.Code code, int retryAfterMs) {
        return ServerFrame.newBuilder().setError(Error.newBuilder().setCode(code).setMessage("test")
                .setFatal(true).setRetryAfterMs(retryAfterMs)).build();
    }

    private static List<String> pandaEyeThreads() {
        return Thread.getAllStackTraces().keySet().stream()
                .filter(Thread::isAlive)
                .map(Thread::getName)
                .filter(name -> name.startsWith("PandaEye-"))
                .toList();
    }

    private static void awaitState(CloudClient client, CloudState state) throws Exception {
        waitUntil(() -> client.state() == state, "state " + state + " (is " + client.state() + ")");
    }

    private static void waitUntil(BooleanSupplier condition, String what) throws Exception {
        long deadline = System.nanoTime() + WAIT.toNanos();
        while (!condition.getAsBoolean()) {
            if (System.nanoTime() > deadline) {
                fail("timed out waiting for " + what);
            }
            Thread.sleep(5);
        }
    }

    private static final class RecordingListener implements CloudListener {
        final List<RemoteSettings> settings = new CopyOnWriteArrayList<>();
        final List<Verdict> verdicts = new CopyOnWriteArrayList<>();
        final List<Long> probeRtts = new CopyOnWriteArrayList<>();

        @Override
        public void onRemoteSettings(RemoteSettings s) {
            settings.add(s);
        }

        @Override
        public void onVerdict(Verdict verdict) {
            verdicts.add(verdict);
        }

        @Override
        public void onProbeResult(ProbeResult result, long rttMicros) {
            probeRtts.add(rttMicros);
        }
    }

}
