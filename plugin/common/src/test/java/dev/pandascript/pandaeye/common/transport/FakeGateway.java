package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.codec.FrameCodec;
import dev.pandascript.pandaeye.protocol.v1.BatchAck;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.Pong;
import dev.pandascript.pandaeye.protocol.v1.RemoteConfig;
import dev.pandascript.pandaeye.protocol.v1.ServerFrame;
import dev.pandascript.pandaeye.protocol.v1.Welcome;

import java.io.ByteArrayOutputStream;
import java.io.DataInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Function;
import java.util.function.Predicate;

/**
 * A minimal WebSocket server over raw sockets that plays the cloud gateway. Each connection gets its own
 * thread (named {@code FakeGateway-*}, never {@code PandaEye-*}). By default it answers Hello with Welcome,
 * acknowledges every batch and answers pings; tests switch behaviours off or script replies.
 */
final class FakeGateway implements AutoCloseable {

    private static final String GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

    private final ServerSocket server;
    private final List<Conn> connections = new CopyOnWriteArrayList<>();
    private final List<Received> received = new CopyOnWriteArrayList<>();
    private final FrameCodec codec = new FrameCodec(new TestLog());
    private final AtomicInteger threadCounter = new AtomicInteger();

    volatile boolean acceptHandshakes = true;
    volatile boolean autoWelcome = true;
    volatile boolean autoAck = true;
    volatile boolean autoPong = true;
    volatile boolean answerClose = true;
    volatile long lastAckedSeq;
    volatile int heartbeatMs = 5_000;
    volatile int maxBatchEvents = 4_096;
    volatile int replayEventsPerSecond = 15_000;
    volatile String serverSecret = "";
    volatile RemoteConfig remoteConfig = RemoteConfig.newBuilder().setLeniency(4).build();
    /** Optional scripted reply to every client frame (in addition to the automatic ones). */
    volatile Function<ClientFrame, List<ServerFrame>> script = frame -> List.of();

    record Received(int connection, ClientFrame frame, long nanos) {
    }

    FakeGateway() throws IOException {
        server = new ServerSocket(0, 50, InetAddress.getLoopbackAddress());
        Thread acceptor = new Thread(this::acceptLoop, "FakeGateway-accept");
        acceptor.setDaemon(true);
        acceptor.start();
    }

    URI uri() {
        return URI.create("ws://127.0.0.1:" + server.getLocalPort() + "/v1/connect");
    }

    int connectionCount() {
        return connections.size();
    }

    Conn connection(int index) {
        return connections.get(index);
    }

    Conn awaitConnection(int index, Duration timeout) throws TimeoutException, InterruptedException {
        long deadline = System.nanoTime() + timeout.toNanos();
        while (connections.size() <= index) {
            if (System.nanoTime() > deadline) {
                throw new TimeoutException("no connection #" + index);
            }
            Thread.sleep(5);
        }
        return connections.get(index);
    }

    List<Received> received() {
        return List.copyOf(received);
    }

    List<ClientFrame> frames(Predicate<ClientFrame> filter) {
        List<ClientFrame> out = new ArrayList<>();
        for (Received r : received) {
            if (filter.test(r.frame())) {
                out.add(r.frame());
            }
        }
        return out;
    }

    Received await(Predicate<ClientFrame> filter, Duration timeout) throws TimeoutException, InterruptedException {
        return awaitAfter(-1, filter, timeout);
    }

    /** Waits for a matching frame received at index > {@code afterIndex} of {@link #received()}. */
    Received awaitAfter(int afterIndex, Predicate<ClientFrame> filter, Duration timeout)
            throws TimeoutException, InterruptedException {
        long deadline = System.nanoTime() + timeout.toNanos();
        while (System.nanoTime() < deadline) {
            List<Received> snapshot = received();
            for (int i = afterIndex + 1; i < snapshot.size(); i++) {
                if (filter.test(snapshot.get(i).frame())) {
                    return snapshot.get(i);
                }
            }
            Thread.sleep(5);
        }
        throw new TimeoutException("no matching frame; received " + received.size() + " frames");
    }

    @Override
    public void close() throws IOException {
        server.close();
        for (Conn conn : connections) {
            conn.kill();
        }
    }

    private void acceptLoop() {
        while (!server.isClosed()) {
            try {
                Socket socket = server.accept();
                socket.setTcpNoDelay(true);
                Thread thread = new Thread(() -> serve(socket), "FakeGateway-conn-" + threadCounter.incrementAndGet());
                thread.setDaemon(true);
                thread.start();
            } catch (IOException e) {
                return;
            }
        }
    }

    private void serve(Socket socket) {
        try {
            InputStream in = socket.getInputStream();
            String key = readHandshake(in);
            if (!acceptHandshakes || key == null) {
                socket.close();
                return;
            }
            OutputStream out = socket.getOutputStream();
            String accept = Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-1")
                    .digest((key + GUID).getBytes(StandardCharsets.US_ASCII)));
            out.write(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                    + "Sec-WebSocket-Accept: " + accept + "\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
            out.flush();
            Conn conn = new Conn(connections.size(), socket, out);
            connections.add(conn);
            conn.readLoop(new DataInputStream(in));
        } catch (Exception e) {
            try {
                socket.close();
            } catch (IOException ignored) {
                // closing
            }
        }
    }

    private static String readHandshake(InputStream in) throws IOException {
        String key = null;
        StringBuilder line = new StringBuilder();
        int previous = -1;
        while (true) {
            int b = in.read();
            if (b < 0) {
                return null;
            }
            if (previous == '\r' && b == '\n') {
                String text = line.substring(0, line.length() - 1);
                if (text.isEmpty()) {
                    return key;
                }
                int colon = text.indexOf(':');
                if (colon > 0 && text.substring(0, colon).trim().toLowerCase(Locale.ROOT).equals("sec-websocket-key")) {
                    key = text.substring(colon + 1).trim();
                }
                line.setLength(0);
            } else {
                line.append((char) b);
            }
            previous = b;
        }
    }

    final class Conn {

        final int index;
        final Socket socket;
        private final OutputStream out;
        volatile boolean closeReceived;

        Conn(int index, Socket socket, OutputStream out) {
            this.index = index;
            this.socket = socket;
            this.out = out;
        }

        void send(ServerFrame frame) throws IOException {
            sendRaw(codec.encodeRaw(frame.toByteArray()));
        }

        void sendRaw(byte[] payload) throws IOException {
            writeFrame(0x82, payload, 0, payload.length);
        }

        /** Sends one binary message split into {@code parts} WebSocket frames. */
        void sendFragmented(ServerFrame frame, int parts) throws IOException {
            byte[] payload = codec.encodeRaw(frame.toByteArray());
            int chunk = Math.max(1, (payload.length + parts - 1) / parts);
            synchronized (out) {
                int offset = 0;
                boolean first = true;
                while (offset < payload.length) {
                    int length = Math.min(chunk, payload.length - offset);
                    boolean fin = offset + length >= payload.length;
                    writeFrame((fin ? 0x80 : 0) | (first ? 0x2 : 0x0), payload, offset, length);
                    offset += length;
                    first = false;
                }
            }
        }

        void sendText(String text) throws IOException {
            byte[] bytes = text.getBytes(StandardCharsets.UTF_8);
            writeFrame(0x81, bytes, 0, bytes.length);
        }

        void sendClose(int code) throws IOException {
            writeFrame(0x88, new byte[]{(byte) (code >> 8), (byte) code}, 0, 2);
        }

        void kill() {
            try {
                socket.close();
            } catch (IOException ignored) {
                // already closed
            }
        }

        private void writeFrame(int firstByte, byte[] payload, int offset, int length) throws IOException {
            synchronized (out) {
                out.write(firstByte);
                if (length < 126) {
                    out.write(length);
                } else if (length < 65536) {
                    out.write(126);
                    out.write(length >> 8);
                    out.write(length);
                } else {
                    out.write(127);
                    for (int shift = 56; shift >= 0; shift -= 8) {
                        out.write((int) ((long) length >> shift));
                    }
                }
                out.write(payload, offset, length);
                out.flush();
            }
        }

        private void readLoop(DataInputStream in) throws Exception {
            ByteArrayOutputStream message = new ByteArrayOutputStream();
            while (true) {
                int b0 = in.readUnsignedByte();
                int b1 = in.readUnsignedByte();
                long length = b1 & 0x7f;
                if (length == 126) {
                    length = in.readUnsignedShort();
                } else if (length == 127) {
                    length = in.readLong();
                }
                byte[] mask = new byte[4];
                if ((b1 & 0x80) != 0) {
                    in.readFully(mask);
                }
                byte[] payload = new byte[(int) length];
                in.readFully(payload);
                for (int i = 0; i < payload.length; i++) {
                    payload[i] ^= mask[i & 3];
                }
                int opcode = b0 & 0x0f;
                boolean fin = (b0 & 0x80) != 0;
                switch (opcode) {
                    case 0x0, 0x2 -> {
                        message.write(payload);
                        if (fin) {
                            handle(message.toByteArray());
                            message.reset();
                        }
                    }
                    case 0x8 -> {
                        closeReceived = true;
                        if (answerClose) {
                            writeFrame(0x88, payload, 0, payload.length);
                            socket.close();
                            return;
                        }
                    }
                    case 0x9 -> writeFrame(0x8a, payload, 0, payload.length);
                    default -> {
                    }
                }
            }
        }

        private void handle(byte[] bytes) throws Exception {
            ClientFrame frame = ClientFrame.parseFrom(codec.decode(bytes));
            received.add(new Received(index, frame, System.nanoTime()));
            switch (frame.getKindCase()) {
                case HELLO -> {
                    if (autoWelcome) {
                        reply(ServerFrame.newBuilder().setWelcome(Welcome.newBuilder()
                                .setConnectionId("conn-" + index)
                                .setTenantId("t_test")
                                .setServerTimeUnixMs(System.currentTimeMillis())
                                .setConfig(remoteConfig)
                                .setHeartbeatIntervalMs(heartbeatMs)
                                .setMaxBatchEvents(maxBatchEvents)
                                .setLastAckedSeq(lastAckedSeq)
                                .setServerSecret(serverSecret)
                                .setMaxEventsPerS(20_000)
                                .setMaxBytesPerS(2 << 20)
                                .setReplayMaxEventsPerS(replayEventsPerSecond))
                                .build());
                    }
                }
                case BATCH -> {
                    if (autoAck) {
                        long seq = frame.getBatch().getSeq();
                        lastAckedSeq = Math.max(lastAckedSeq, seq);
                        reply(ServerFrame.newBuilder().setBatchAck(BatchAck.newBuilder().setSeq(seq)).build());
                    }
                }
                case PING -> {
                    if (autoPong) {
                        reply(ServerFrame.newBuilder().setPong(Pong.newBuilder()
                                .setNonce(frame.getPing().getNonce())
                                .setClientTimeUs(frame.getPing().getClientTimeUs())
                                .setServerTimeUs(System.currentTimeMillis() * 1000)
                                .setConfig(remoteConfig)).build());
                    }
                }
                default -> {
                }
            }
            for (ServerFrame scripted : script.apply(frame)) {
                reply(scripted);
            }
        }

        /**
         * Answers a client frame. A client that has gone (it aborts right after its close frame) makes the write
         * fail, but what it sent before is still buffered here and is read on: Goodbye and close frame included.
         */
        private void reply(ServerFrame frame) {
            try {
                send(frame);
            } catch (IOException e) {
                // the client is gone; keep reading its buffered frames until the stream ends
            }
        }
    }
}
