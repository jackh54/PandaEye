package dev.pandascript.pandaeye.common.transport;

import com.google.protobuf.InvalidProtocolBufferException;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.codec.FrameCodec;
import dev.pandascript.pandaeye.common.codec.FrameException;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.util.DaemonThreads;
import dev.pandascript.pandaeye.common.util.Fingerprint;
import dev.pandascript.pandaeye.common.util.TokenBucket;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.Error;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.Goodbye;
import dev.pandascript.pandaeye.protocol.v1.Hello;
import dev.pandascript.pandaeye.protocol.v1.Ping;
import dev.pandascript.pandaeye.protocol.v1.Pong;
import dev.pandascript.pandaeye.protocol.v1.Probe;
import dev.pandascript.pandaeye.protocol.v1.ProbeResult;
import dev.pandascript.pandaeye.protocol.v1.RemoteConfig;
import dev.pandascript.pandaeye.protocol.v1.ServerFrame;
import dev.pandascript.pandaeye.protocol.v1.VerdictAck;
import dev.pandascript.pandaeye.protocol.v1.Welcome;

import java.io.IOException;
import java.net.http.HttpClient;
import java.net.http.WebSocket;
import java.nio.ByteBuffer;
import java.time.Duration;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentLinkedQueue;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.function.IntSupplier;
import java.util.SplittableRandom;
import java.util.random.RandomGenerator;

/**
 * The plugin side of the cloud connection (ARCHITECTURE 3 and 4.6). All network I/O and all transport state
 * live on one daemon thread, {@code PandaEye-Cloud}; the JDK HttpClient runs on daemon {@code PandaEye-Http-*}
 * threads and {@code buildAsync} is only called from the cloud thread. Producers hand events to
 * {@link #sink()}; the cloud thread batches, compresses, sends (one send outstanding), resends what was not
 * acknowledged after a reconnect, paces replayed data, and spools what is left on {@link #stop}.
 */
public final class CloudClient {

    public static final List<String> CAPABILITIES = List.of("tick_end", "env_sampling", "target_history", "probe");
    static final int PROTOCOL_VERSION = 1;
    static final int DEFAULT_HEARTBEAT_MS = 5_000;
    static final int DEFAULT_MAX_BATCH_EVENTS = 4_096;
    static final int DEFAULT_REPLAY_EVENTS_PER_S = 15_000;
    static final int MAX_INFLIGHT_BATCHES = 128;
    static final int MAX_BATCHES_PER_TICK = 16;
    private static final int CLOSE_GOING_AWAY = 1001;
    private static final long CLOSE_WAIT_NANOS = 100_000_000L;
    private static final byte[] GOODBYE = ClientFrame.newBuilder()
            .setGoodbye(Goodbye.newBuilder().setReason("plugin stopping")).build().toByteArray();
    private static final long REMOTE_PERSIST_REFRESH_NANOS = 600_000_000_000L;

    /** Protocol timings; tests shorten them. */
    record Timings(Duration connectTimeout, Duration helloTimeout, Duration probeInterval, long backoffBaseMillis,
                   long backoffMaxMillis, long authWaitMillis, long duplicateWaitMillis, Duration networkStop) {

        static final Timings DEFAULT = new Timings(Duration.ofSeconds(5), Duration.ofSeconds(10),
                Duration.ofSeconds(10), 1_000, 60_000, 600_000, 30_000, Duration.ofMillis(200));
    }

    private final CloudConfig config;
    private final CloudListener listener;
    private final Log log;
    private final EventClock clock;
    private final IntSupplier onlinePlayers;
    private final Timings timings;
    private final FrameCodec codec;
    private final String instanceId = UUID.randomUUID().toString();
    private final EventSink sink = new EventSink(EventSink.DEFAULT_CAPACITY);
    private final PendingBuffer buffer;
    private final BatchBuilder batches = new BatchBuilder();
    private final ProbeTracker probes = new ProbeTracker();
    private final Backoff backoff;
    private final TokenBucket replayPacer;
    private final ConcurrentLinkedQueue<VerdictAck> verdictAcks = new ConcurrentLinkedQueue<>();
    private final DaemonThreads loopThreads = DaemonThreads.named("PandaEye-Cloud");
    private final DaemonThreads httpThreads = DaemonThreads.numbered("PandaEye-Http-");
    private final Object lifecycle = new Object();

    private volatile ScheduledExecutorService loop;
    private volatile ExecutorService httpExecutor;
    private volatile boolean stopping;
    private volatile boolean initialised;
    private volatile RemoteSettings remote = RemoteSettings.UNKNOWN;
    private volatile CloudState state = CloudState.STOPPED;
    private volatile String stateDetail = "";
    private volatile CompletableFuture<WebSocket> connecting;
    private volatile WebSocket socket;
    private volatile long connectionRttMicros;
    private volatile long lastAckedSeq;
    private volatile long sentBatches;
    private volatile long tooLargeBatches;

    // Cloud-thread state.
    private volatile HttpClient http;
    private ServerIdentity identity;
    private StateStore stateStore;
    private SeqAllocator seqs;
    private RemoteSettingsStore remoteStore;
    private Spool spool;
    private int generation;
    private boolean sendInFlight;
    private boolean pingDue;
    private long pingNonce;
    private long phaseStartedNanos;
    private long lastFrameNanos;
    private long lastPingNanos;
    private long lastProbeNanos;
    private long reconnectAtNanos;
    private long lastRemotePersistNanos;
    private long heartbeatNanos = TimeUnit.MILLISECONDS.toNanos(DEFAULT_HEARTBEAT_MS);
    private int maxBatchEvents = DEFAULT_MAX_BATCH_EVENTS;
    private long delayOverrideMillis = -1;
    private String loggedReason = "";
    private int failuresSinceReady;

    public CloudClient(CloudConfig config, CloudListener listener, Log log, EventClock clock, IntSupplier onlinePlayers) {
        // Not RandomGenerator.getDefault(): it looks its algorithm up in the jdk.random module, which paperclip's
        // class loader cannot see ("No implementation of ... L32X64MixRandom"). SplittableRandom is in java.base.
        this(config, listener, log, clock, onlinePlayers, Timings.DEFAULT, new FrameCodec(log), new SplittableRandom());
    }

    CloudClient(CloudConfig config, CloudListener listener, Log log, EventClock clock, IntSupplier onlinePlayers,
                Timings timings, FrameCodec codec, RandomGenerator random) {
        this.config = config;
        this.listener = listener;
        this.log = log;
        this.clock = clock;
        this.onlinePlayers = onlinePlayers;
        this.timings = timings;
        this.codec = codec;
        this.buffer = new PendingBuffer(config.maxBufferBytes(), config.maxBufferAge().toNanos());
        this.backoff = new Backoff(timings.backoffBaseMillis(), timings.backoffMaxMillis(), random);
        this.replayPacer = new TokenBucket(DEFAULT_REPLAY_EVENTS_PER_S / 2.0, DEFAULT_MAX_BATCH_EVENTS, clock.nanoTime());
    }

    /** Where producers put events. Never blocks; drops (and counts) when full. */
    public EventSink sink() {
        return sink;
    }

    public String instanceId() {
        return instanceId;
    }

    /** The last-known tenant settings; {@link RemoteSettings#UNKNOWN} (enforcement off) until loaded or received. */
    public RemoteSettings remoteSettings() {
        return remote;
    }

    public CloudState state() {
        return state;
    }

    /** Thread-safe: queues a VerdictAck for sending. */
    public void ackVerdict(String verdictId, boolean executed, String note) {
        verdictAcks.offer(VerdictAck.newBuilder().setVerdictId(verdictId).setExecuted(executed)
                .setNote(note == null ? "" : note).build());
        post(this::pump);
    }

    public CloudStatus status() {
        ServerIdentity id = identity;
        return new CloudStatus(state, stateDetail, id == null ? "" : id.serverId(), connectionRttMicros,
                probes.lastRttMicros(), probes.percentile(50), probes.percentile(95), buffer.batchCount(),
                buffer.replayCount(), buffer.bytes(), sink.size(), sink.dropped(), buffer.droppedBatches(),
                buffer.droppedEvents(), batches.oversizeDropped(), tooLargeBatches, sentBatches, lastAckedSeq,
                codec.compressionCodec() == FrameCodec.CODEC_ZSTD ? "zstd" : "deflate");
    }

    public void start() {
        synchronized (lifecycle) {
            if (loop != null) {
                throw new IllegalStateException("already started");
            }
            ScheduledThreadPoolExecutor executor = new ScheduledThreadPoolExecutor(1, loopThreads);
            executor.setRemoveOnCancelPolicy(true);
            executor.setExecuteExistingDelayedTasksAfterShutdownPolicy(false);
            httpExecutor = Executors.newCachedThreadPool(httpThreads);
            loop = executor;
            state = CloudState.STARTING;
            long interval = config.batchInterval().toMillis();
            executor.execute(this::initialise);
            executor.scheduleWithFixedDelay(this::tick, interval, interval, TimeUnit.MILLISECONDS);
        }
    }

    /**
     * Bounded shutdown (ARCHITECTURE 4.6): stop the loop, cancel a pending connect, best-effort Goodbye and
     * close frame (100 ms together), always abort, then {@code HttpClient.shutdownNow()} (non-blocking; its
     * SelectorManager thread would otherwise outlive the plugin and pin the class loader) and stop the
     * HttpClient executor; never {@code HttpClient.close()}. The remaining budget spools unacknowledged batches
     * and undrained events.
     */
    public void stop(Duration budget) {
        long start = System.nanoTime();
        long deadline = start + budget.toNanos();
        long networkDeadline = start + Math.min(budget.toNanos(), timings.networkStop().toNanos());
        ScheduledExecutorService loopRef;
        ExecutorService httpRef;
        synchronized (lifecycle) {
            if (loop == null || stopping) {
                return;
            }
            stopping = true;
            loopRef = loop;
            httpRef = httpExecutor;
        }
        loopRef.shutdownNow();
        CompletableFuture<WebSocket> pending = connecting;
        if (pending != null) {
            pending.cancel(true);
            pending.whenComplete(CloudClient::abortQuietly);
        }
        WebSocket ws = socket;
        if (ws != null) {
            closeGracefully(ws, codec.encodeRaw(GOODBYE), Math.min(start + CLOSE_WAIT_NANOS, networkDeadline));
            ws.abort();
        }
        HttpClient client = http;
        if (client != null) {
            client.shutdownNow();
        }
        httpRef.shutdownNow();
        boolean loopStopped = awaitQuietly(loopRef, networkDeadline);
        HttpClient late = http; // created by a connect that was still running on the loop
        if (late != null && late != client) {
            late.shutdownNow();
        }
        awaitQuietly(httpRef, networkDeadline);
        awaitQuietly(late != null ? late : client, networkDeadline);
        loopStopped &= loopThreads.joinAll(networkDeadline);
        httpThreads.joinAll(networkDeadline);
        state = CloudState.STOPPED;
        persistOnStop(loopStopped, deadline);
    }

    // ---------------------------------------------------------------------------------------------
    // Callbacks from Connection (HttpClient threads): hand everything to the cloud thread.
    // ---------------------------------------------------------------------------------------------

    void onMessage(int gen, byte[] message) {
        post(() -> handleMessage(gen, message));
    }

    void onClosed(int gen, int code, String reason) {
        post(() -> {
            if (gen == generation) {
                fail("connection closed by the cloud (" + code + (reason.isEmpty() ? "" : " " + reason) + ")", -1);
            }
        });
    }

    void onTransportError(int gen, Throwable error) {
        post(() -> {
            if (gen == generation) {
                fail("connection error: " + describe(error), -1);
            }
        });
    }

    void onProtocolViolation(int gen, String reason) {
        post(() -> {
            if (gen == generation) {
                fail(reason, -1);
            }
        });
    }

    // ---------------------------------------------------------------------------------------------
    // Cloud thread.
    // ---------------------------------------------------------------------------------------------

    private void initialise() {
        try {
            identity = ServerIdentity.load(config.dataDir(), log);
            stateStore = new StateStore(config.dataDir().resolve("state.properties"), log);
            if (!stateStore.wasCleanShutdown()) {
                log.info("the previous shutdown was not clean; resuming above the reserved seq block");
            }
            seqs = new SeqAllocator(stateStore);
            remoteStore = new RemoteSettingsStore(config.dataDir().resolve("remote-config.properties"), log);
            RemoteSettings stored = remoteStore.load();
            if (stored.known()) {
                remote = stored;
                notifyListener(() -> listener.onRemoteSettings(stored));
            }
            spool = new Spool(config.dataDir().resolve("spool"), log, Spool.MAX_BYTES);
            List<PendingBatch> restored = spool.load(clock);
            if (!restored.isEmpty()) {
                buffer.restore(restored);
                log.info("restored " + restored.size() + " spooled batches; they are resent as replayed data");
            }
            log.info("cloud transport: endpoint " + config.endpoint() + ", server id " + identity.serverId()
                    + ", license " + Fingerprint.of(config.licenseKey()) + ", instance " + instanceId);
            initialised = true;
            setState(CloudState.WAITING, "starting");
            reconnectAtNanos = clock.nanoTime();
        } catch (IOException | RuntimeException e) {
            log.warn("cloud transport could not initialise its data files (retrying in 60 s): " + e);
            reconnectAtNanos = clock.nanoTime() + TimeUnit.SECONDS.toNanos(60);
        }
    }

    private void tick() {
        try {
            if (stopping) {
                return;
            }
            long now = clock.nanoTime();
            if (!initialised) {
                if (now - reconnectAtNanos >= 0) {
                    initialise();
                }
                return;
            }
            if (state == CloudState.READY && now - lastProbeNanos >= timings.probeInterval().toNanos()) {
                lastProbeNanos = now;
                sink.offer(Event.newBuilder().setTimeUs(clock.toMicros(now))
                        .setProbe(Probe.newBuilder().setProbeId(probes.issue(now))).build());
            }
            drainSink(now);
            buffer.expire(now);
            switch (state) {
                case WAITING -> {
                    if (now - reconnectAtNanos >= 0) {
                        connect(now);
                    }
                }
                case CONNECTING -> {
                    if (now - phaseStartedNanos > timings.connectTimeout().toNanos() + timings.helloTimeout().toNanos()) {
                        fail("connecting timed out", -1);
                    }
                }
                case HANDSHAKING -> {
                    if (now - phaseStartedNanos > timings.helloTimeout().toNanos()) {
                        fail("no Welcome within " + timings.helloTimeout().toSeconds() + " s", -1);
                    }
                }
                case READY -> {
                    if (now - lastFrameNanos > 3 * heartbeatNanos) {
                        fail("no frame from the cloud for " + TimeUnit.NANOSECONDS.toMillis(3 * heartbeatNanos) + " ms", -1);
                        return;
                    }
                    if (now - lastPingNanos >= heartbeatNanos) {
                        lastPingNanos = now;
                        pingDue = true;
                    }
                    pump();
                }
                default -> {
                }
            }
        } catch (Throwable t) {
            log.warn("cloud transport tick failed", t);
        }
    }

    private void drainSink(long now) {
        boolean connected = state == CloudState.READY;
        for (int i = 0; i < MAX_BATCHES_PER_TICK; i++) {
            PendingBatch batch = batches.next(sink, maxBatchEvents, now, clock.nowMillis());
            if (batch == null) {
                return;
            }
            buffer.add(batch, connected);
        }
    }

    private void connect(long now) {
        int gen = ++generation;
        phaseStartedNanos = now;
        setState(CloudState.CONNECTING, "connecting to " + config.endpoint().getHost());
        try {
            if (http == null) {
                http = HttpClient.newBuilder()
                        .connectTimeout(timings.connectTimeout())
                        .executor(httpExecutor)
                        .build();
            }
            CompletableFuture<WebSocket> future = http.newWebSocketBuilder()
                    .connectTimeout(timings.connectTimeout())
                    .buildAsync(config.endpoint(), new Connection(this, gen, FrameCodec.MAX_COMPRESSED_BYTES + 1));
            connecting = future;
            future.whenComplete((ws, error) -> post(() -> onConnectResult(gen, ws, error)));
        } catch (RuntimeException e) {
            fail("could not start the connection: " + describe(e), -1);
        }
    }

    private void onConnectResult(int gen, WebSocket ws, Throwable error) {
        if (gen != generation || state != CloudState.CONNECTING) {
            if (ws != null) {
                ws.abort();
            }
            return;
        }
        if (error != null || ws == null) {
            connecting = null;
            fail("could not connect: " + describe(error), -1);
            return;
        }
        socket = ws;
        connecting = null;
        long now = clock.nanoTime();
        phaseStartedNanos = now;
        lastFrameNanos = now;
        setState(CloudState.HANDSHAKING, "authenticating");
        send(codec.encodeRaw(ClientFrame.newBuilder().setHello(hello()).build().toByteArray()));
    }

    private Hello hello() {
        return Hello.newBuilder()
                .setProtocolVersion(PROTOCOL_VERSION)
                .setLicenseKey(config.licenseKey())
                .setServerId(identity.serverId())
                .setServerName(config.serverName())
                .setNetworkId(config.networkId())
                .setPlatform(config.platform())
                .setPlatformVersion(config.platformVersion())
                .setPluginVersion(config.pluginVersion())
                .setMinecraftVersion(config.minecraftVersion())
                .setShadowMode(config.shadowMode())
                .setClientTimeUnixMs(clock.nowMillis())
                .addAllCapabilities(config.capabilities())
                .setOnlinePlayers(Math.max(0, onlinePlayers.getAsInt()))
                .setInstanceId(instanceId)
                .setServerSecret(identity.secret())
                .build();
    }

    private void handleMessage(int gen, byte[] message) {
        if (gen != generation || socket == null) {
            return;
        }
        lastFrameNanos = clock.nanoTime();
        ServerFrame frame;
        try {
            frame = ServerFrame.parseFrom(codec.decode(message));
        } catch (FrameException | InvalidProtocolBufferException e) {
            fail("undecodable frame from the cloud: " + e.getMessage(), -1);
            return;
        }
        if (frame.getKindCase() == ServerFrame.KindCase.ERROR) {
            handleError(frame.getError());
            return;
        }
        if (state == CloudState.HANDSHAKING) {
            if (frame.getKindCase() == ServerFrame.KindCase.WELCOME) {
                handleWelcome(frame.getWelcome());
            }
            return;
        }
        if (state != CloudState.READY) {
            return;
        }
        switch (frame.getKindCase()) {
            case BATCH_ACK -> {
                long seq = frame.getBatchAck().getSeq();
                buffer.ack(seq);
                lastAckedSeq = Math.max(lastAckedSeq, seq);
                pump();
            }
            case PONG -> handlePong(frame.getPong());
            case VERDICT -> notifyListener(() -> listener.onVerdict(frame.getVerdict()));
            case PROBE_RESULT -> {
                ProbeResult result = frame.getProbeResult();
                long rtt = probes.complete(result.getProbeId(), clock.nanoTime());
                if (rtt >= 0) {
                    notifyListener(() -> listener.onProbeResult(result, rtt));
                }
            }
            case WATCH -> notifyListener(() -> listener.onWatch(frame.getWatch()));
            case PLAYER_RISK -> notifyListener(() -> listener.onPlayerRisk(frame.getPlayerRisk()));
            case CONFIG_UPDATE -> applyRemote(frame.getConfigUpdate());
            default -> {
                // WELCOME after READY or an unknown kind: ignored for forward compatibility.
            }
        }
    }

    private void handleWelcome(Welcome welcome) {
        heartbeatNanos = TimeUnit.MILLISECONDS.toNanos(
                welcome.getHeartbeatIntervalMs() > 0 ? welcome.getHeartbeatIntervalMs() : DEFAULT_HEARTBEAT_MS);
        maxBatchEvents = welcome.getMaxBatchEvents() > 0 ? welcome.getMaxBatchEvents() : DEFAULT_MAX_BATCH_EVENTS;
        long acked = welcome.getLastAckedSeq();
        buffer.onConnected(acked);
        seqs.ensureAbove(acked);
        lastAckedSeq = acked;
        if (!welcome.getServerSecret().isEmpty()) {
            try {
                identity.saveSecret(welcome.getServerSecret());
                log.info("registered this server with the cloud (server id " + identity.serverId() + ")");
            } catch (IOException e) {
                log.warn("could not store the server secret; the next connection will be rejected: " + e);
            }
        }
        int replayBudget = welcome.getReplayMaxEventsPerS() > 0 ? welcome.getReplayMaxEventsPerS() : DEFAULT_REPLAY_EVENTS_PER_S;
        replayPacer.configure(replayBudget / 2.0, Math.max(replayBudget / 2.0, maxBatchEvents));
        if (welcome.hasConfig()) {
            applyRemote(welcome.getConfig());
        }
        backoff.reset();
        delayOverrideMillis = -1;
        long now = clock.nanoTime();
        lastPingNanos = now;
        lastProbeNanos = now - timings.probeInterval().toNanos();
        String detail = "connected (tenant " + welcome.getTenantId() + ")"
                + (failuresSinceReady > 1 ? " after " + failuresSinceReady + " failed attempts" : "");
        failuresSinceReady = 0;
        setState(CloudState.READY, detail);
        pump();
    }

    private void handlePong(Pong pong) {
        if (pong.getClientTimeUs() > 0) {
            connectionRttMicros = Math.max(0, clock.nowMicros() - pong.getClientTimeUs());
        }
        if (pong.hasConfig()) {
            applyRemote(pong.getConfig());
        }
    }

    private void applyRemote(RemoteConfig config) {
        RemoteSettings previous = remote;
        RemoteSettings next = RemoteSettings.of(config, clock.nowMillis());
        remote = next;
        long now = clock.nanoTime();
        boolean changed = !previous.known() || !previous.sameValues(next);
        if (changed || now - lastRemotePersistNanos > REMOTE_PERSIST_REFRESH_NANOS) {
            lastRemotePersistNanos = now;
            remoteStore.save(next);
        }
        if (changed) {
            if (previous.enforcement() != next.enforcement()) {
                log.info("cloud enforcement switch is now " + (next.enforcement() ? "ON" : "OFF"));
            }
            notifyListener(() -> listener.onRemoteSettings(next));
        }
    }

    private void handleError(Error error) {
        long retryAfter = error.getRetryAfterMs();
        String text = "cloud error " + error.getCode().name().replace("CODE_", "") + ": " + error.getMessage();
        switch (error.getCode()) {
            case CODE_UNAUTHENTICATED, CODE_LICENSE_LIMIT, CODE_UNSUPPORTED_PROTOCOL -> {
                delayOverrideMillis = Math.max(timings.authWaitMillis(), retryAfter);
                text += " (license " + Fingerprint.of(config.licenseKey()) + "); retrying in "
                        + TimeUnit.MILLISECONDS.toMinutes(delayOverrideMillis) + " min, buffered events are kept";
            }
            case CODE_DUPLICATE_SERVER -> {
                delayOverrideMillis = Math.max(timings.duplicateWaitMillis(), retryAfter);
                log.warn("!!! Another server is connected with this server id (" + identity.serverId() + "). If this"
                        + " server's plugins/PandaEye folder was copied from another server, stop this server, delete"
                        + " plugins/PandaEye/server-id and plugins/PandaEye/server-secret, and start it again. !!!");
            }
            case CODE_RATE_LIMITED, CODE_INTERNAL, CODE_SHUTTING_DOWN -> {
                if (retryAfter > 0) {
                    delayOverrideMillis = retryAfter;
                }
            }
            case CODE_TOO_LARGE -> tooLargeBatches++;
            default -> {
            }
        }
        if (error.getFatal()) {
            fail(text, delayOverrideMillis);
        } else if (error.getCode() != Error.Code.CODE_TOO_LARGE) {
            log.warn(text);
        }
    }

    private void pump() {
        if (state != CloudState.READY || sendInFlight || socket == null) {
            return;
        }
        byte[] frame = nextFrame(clock.nanoTime());
        if (frame != null) {
            send(frame);
        }
    }

    private byte[] nextFrame(long now) {
        VerdictAck ack = verdictAcks.poll();
        if (ack != null) {
            return codec.encode(ClientFrame.newBuilder().setVerdictAck(ack).build().toByteArray());
        }
        if (pingDue) {
            pingDue = false;
            Ping ping = Ping.newBuilder()
                    .setNonce(++pingNonce)
                    .setClientTimeUs(clock.toMicros(now))
                    .setLastProbeRttUs((int) Math.min(Integer.MAX_VALUE, probes.freshRttMicros(now)))
                    .setOnlinePlayers(Math.max(0, onlinePlayers.getAsInt()))
                    .build();
            return codec.encode(ClientFrame.newBuilder().setPing(ping).build().toByteArray());
        }
        PendingBatch batch = buffer.nextResend(generation);
        if (batch == null && buffer.inflightCount() < MAX_INFLIGHT_BATCHES) {
            batch = buffer.pollLive();
            if (batch == null) {
                PendingBatch head = buffer.peekReplay();
                if (head != null && replayPacer.tryAcquire(now, head.eventCount)) {
                    batch = buffer.pollReplay();
                }
            }
            if (batch != null && batch.seq == 0) {
                batch.seq = seqs.next();
            }
        }
        if (batch == null) {
            return null;
        }
        buffer.markSent(batch, generation);
        sentBatches++;
        return codec.encode(Frames.batch(batch, clock.nowMillis()));
    }

    private void send(byte[] frame) {
        WebSocket ws = socket;
        int gen = generation;
        sendInFlight = true;
        try {
            ws.sendBinary(ByteBuffer.wrap(frame), true)
                    .whenComplete((ignored, error) -> post(() -> onSendComplete(gen, error)));
        } catch (RuntimeException e) {
            sendInFlight = false;
            fail("send failed: " + describe(e), -1);
        }
    }

    private void onSendComplete(int gen, Throwable error) {
        if (gen != generation) {
            return;
        }
        sendInFlight = false;
        if (error != null) {
            fail("send failed: " + describe(error), -1);
            return;
        }
        pump();
    }

    private void fail(String reason, long delayMillis) {
        WebSocket ws = socket;
        socket = null;
        if (ws != null) {
            ws.abort();
        }
        CompletableFuture<WebSocket> pending = connecting;
        connecting = null;
        if (pending != null) {
            pending.cancel(true);
            pending.whenComplete(CloudClient::abortQuietly);
        }
        generation++;
        sendInFlight = false;
        pingDue = false;
        long delay = delayMillis >= 0 ? delayMillis : delayOverrideMillis >= 0 ? delayOverrideMillis : backoff.nextDelayMillis();
        delayOverrideMillis = -1;
        reconnectAtNanos = clock.nanoTime() + TimeUnit.MILLISECONDS.toNanos(delay);
        failuresSinceReady++;
        setState(CloudState.WAITING, reason, " - retrying in " + String.format(java.util.Locale.ROOT, "%.1f s", delay / 1000.0));
    }

    private void setState(CloudState next, String reason) {
        setState(next, reason, "");
    }

    /** Logs a state change once: repeated failures with the same reason are not logged again. */
    private void setState(CloudState next, String reason, String transientSuffix) {
        state = next;
        stateDetail = reason + transientSuffix;
        String key = next + ":" + reason;
        if (!key.equals(loggedReason) && next != CloudState.CONNECTING && next != CloudState.HANDSHAKING) {
            loggedReason = key;
            if (next == CloudState.WAITING && !reason.equals("starting")) {
                log.warn("cloud: " + reason + transientSuffix + " (events are buffered meanwhile)");
            } else if (next == CloudState.READY) {
                log.info("cloud: " + reason);
            }
        }
        String detail = stateDetail;
        notifyListener(() -> listener.onStateChanged(next, detail));
    }

    private void persistOnStop(boolean loopStopped, long deadline) {
        if (!initialised) {
            return;
        }
        List<PendingBatch> pending = buffer.drainAll();
        if (loopStopped) {
            long now = System.nanoTime();
            PendingBatch batch;
            while ((batch = batches.next(sink, maxBatchEvents, now, clock.nowMillis())) != null) {
                pending.add(batch);
            }
        }
        spool.write(pending, deadline);
        stateStore.markCleanShutdown();
    }

    private void notifyListener(Runnable call) {
        try {
            call.run();
        } catch (Throwable t) {
            log.warn("cloud listener failed", t);
        }
    }

    private void post(Runnable task) {
        ScheduledExecutorService executor = loop;
        if (executor == null || stopping) {
            return;
        }
        try {
            executor.execute(task);
        } catch (RejectedExecutionException ignored) {
            // stopping
        }
    }

    /** Goodbye, then a close frame (1001), together bounded by the deadline; best effort. */
    private static void closeGracefully(WebSocket ws, byte[] goodbye, long deadlineNanos) {
        try {
            ws.sendBinary(ByteBuffer.wrap(goodbye), true)
                    .exceptionally(error -> ws)
                    .thenCompose(socket -> socket.sendClose(CLOSE_GOING_AWAY, "plugin stopping"))
                    .get(Math.max(1, deadlineNanos - System.nanoTime()), TimeUnit.NANOSECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (Exception ignored) {
            // best effort: the socket is aborted right after
        }
    }

    private static void abortQuietly(WebSocket ws, Throwable ignored) {
        if (ws != null) {
            ws.abort();
        }
    }

    private static boolean awaitQuietly(ExecutorService executor, long deadlineNanos) {
        try {
            return executor.awaitTermination(Math.max(0, deadlineNanos - System.nanoTime()), TimeUnit.NANOSECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return executor.isTerminated();
        }
    }

    /** Waits (bounded) for a shut-down HttpClient's SelectorManager thread to end. */
    private static void awaitQuietly(HttpClient client, long deadlineNanos) {
        if (client == null) {
            return;
        }
        try {
            client.awaitTermination(Duration.ofNanos(Math.max(0, deadlineNanos - System.nanoTime())));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static String describe(Throwable error) {
        if (error == null) {
            return "unknown error";
        }
        Throwable root = error;
        while (root.getCause() != null && root.getCause() != root) {
            root = root.getCause();
        }
        String message = root.getMessage();
        return root.getClass().getSimpleName() + (message == null ? "" : ": " + message);
    }
}
