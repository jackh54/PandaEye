package dev.pandascript.pandaeye.e2e.probe;

import com.github.retrooper.packetevents.PacketEvents;
import com.github.retrooper.packetevents.protocol.ConnectionState;
import com.github.retrooper.packetevents.protocol.PacketSide;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.packettype.PacketTypeCommon;
import com.github.retrooper.packetevents.protocol.player.ClientVersion;
import com.github.retrooper.packetevents.protocol.player.User;
import io.netty.buffer.ByteBuf;
import io.netty.channel.Channel;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInboundHandlerAdapter;
import io.netty.channel.ChannelOutboundHandlerAdapter;
import io.netty.channel.ChannelPipeline;
import io.netty.channel.ChannelPromise;

import java.util.ArrayDeque;
import java.util.Iterator;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.zip.CRC32;
import java.util.zip.DataFormatException;
import java.util.zip.Inflater;

/**
 * Raw netty taps. Nothing here modifies, delays, cancels or re-orders a packet: every handler only reads the
 * readable bytes with absolute getters (reader/writer indices untouched) and forwards the original message.
 *
 * <pre>
 * outbound flow: packet_handler -> encoder -> [TAP_OUT_ENC] -> (via/pe encoders) -> compress -> prepender -> [TAP_OUT_WIRE] -> socket
 * inbound flow : socket -> [TAP_IN_WIRE] -> splitter -> decompress -> [TAP_IN_PRE] -> (via/pe decoders) -> [TAP_IN_DEC] -> decoder
 * </pre>
 *
 * TAP_OUT_WIRE / TAP_IN_WIRE are the first handlers of the pipeline (closest to the socket), so they count exactly
 * what goes on / comes off the wire. They parse frames themselves (VarInt length, optional zlib compression).
 * TAP_OUT_ENC vs TAP_OUT_WIRE (and TAP_IN_WIRE vs TAP_IN_DEC) are compared by CRC32 of the packet bytes to count
 * packets that were modified, dropped (cancelled) or injected in between, i.e. by PacketEvents listeners
 * (including PandaEye's) and other netty handlers.
 */
final class WireTaps {
    static final String TAP_OUT_WIRE = "pe-e2e-probe-out-wire";
    static final String TAP_IN_WIRE = "pe-e2e-probe-in-wire";
    static final String TAP_OUT_ENC = "pe-e2e-probe-out-enc";
    static final String TAP_IN_PRE = "pe-e2e-probe-in-pre";
    static final String TAP_IN_DEC = "pe-e2e-probe-in-dec";
    private static final int MAX_QUEUE = 8192;
    private static final int SEARCH_AHEAD = 64;

    private final ProbePlugin probe;
    private final Stats stats;
    final ConcurrentHashMap<Channel, Conn> conns = new ConcurrentHashMap<>();

    WireTaps(ProbePlugin probe, Stats stats) {
        this.probe = probe;
        this.stats = stats;
    }

    /** Per-connection state. All mutable fields are confined to the channel's event loop. */
    final class Conn {
        final Channel channel;
        volatile String name = "?";
        volatile ClientVersion clientVersion;
        volatile ClientVersion serverProtocolVersion;
        ConnectionState outState = ConnectionState.PLAY;
        ConnectionState inState = ConnectionState.PLAY;
        final FrameParser outParser = new FrameParser();
        final FrameParser inParser = new FrameParser();
        final ArrayDeque<long[]> outQ = new ArrayDeque<>();
        final ArrayDeque<long[]> inQ = new ArrayDeque<>();
        boolean outEncSeen;
        volatile boolean relativeAttached;
        long inT0;
        volatile List<String> pipelineAtAttach = List.of();

        Conn(Channel channel) {
            this.channel = channel;
        }

        String who() {
            return name;
        }
    }

    // ------------------------------------------------------------------------------------------------
    // attach
    // ------------------------------------------------------------------------------------------------

    /** Attach the wire taps (head of the pipeline). Safe to call several times. */
    void attachWire(User user) {
        Object ch = user.getChannel();
        if (!(ch instanceof Channel channel)) return;
        channel.eventLoop().execute(() -> {
            try {
                if (!channel.isActive()) return;
                Conn c = conns.computeIfAbsent(channel, Conn::new);
                if (user.getName() != null) c.name = user.getName();
                c.clientVersion = user.getClientVersion();
                c.serverProtocolVersion = PacketEvents.getAPI().getServerManager().getVersion().toClientVersion();
                ConnectionState enc = user.getEncoderState();
                ConnectionState dec = user.getDecoderState();
                if (enc != null) c.outState = enc;
                if (dec != null) c.inState = dec;
                ChannelPipeline p = channel.pipeline();
                if (p.get(TAP_OUT_WIRE) == null) p.addFirst(TAP_OUT_WIRE, new OutWire(c));
                if (p.get(TAP_IN_WIRE) == null) p.addFirst(TAP_IN_WIRE, new InWire(c));
                channel.closeFuture().addListener(f -> conns.remove(channel));
            } catch (Throwable t) {
                probe.warn("attachWire", t);
            }
        });
    }

    /** Attach the relative taps around the encoder/decoder chain (in PLAY, after compression was set up). */
    void attachRelative(Object rawChannel, String name) {
        if (!(rawChannel instanceof Channel channel)) return;
        channel.eventLoop().execute(() -> {
            try {
                if (!channel.isActive()) return;
                Conn c = conns.computeIfAbsent(channel, Conn::new);
                if (name != null) c.name = name;
                ChannelPipeline p = channel.pipeline();
                if (p.get(TAP_OUT_WIRE) == null) p.addFirst(TAP_OUT_WIRE, new OutWire(c));
                if (p.get(TAP_IN_WIRE) == null) p.addFirst(TAP_IN_WIRE, new InWire(c));
                if (!c.relativeAttached) {
                    if (p.get("encoder") != null && p.get(TAP_OUT_ENC) == null) p.addBefore("encoder", TAP_OUT_ENC, new OutEnc(c));
                    if (p.get("decoder") != null && p.get(TAP_IN_DEC) == null) p.addBefore("decoder", TAP_IN_DEC, new InDec(c));
                    String pre = p.get("decompress") != null ? "decompress" : (p.get("splitter") != null ? "splitter" : null);
                    if (pre != null && p.get(TAP_IN_PRE) == null) p.addAfter(pre, TAP_IN_PRE, new InPre(c));
                    c.inQ.clear();
                    c.relativeAttached = true;
                }
                c.pipelineAtAttach = List.copyOf(p.names());
                probe.debug("pipeline " + c.name + ": " + p.names());
            } catch (Throwable t) {
                probe.warn("attachRelative", t);
            }
        });
    }

    void detachAll() {
        for (Conn c : conns.values()) {
            Channel ch = c.channel;
            ch.eventLoop().execute(() -> {
                for (String n : new String[]{TAP_OUT_WIRE, TAP_IN_WIRE, TAP_OUT_ENC, TAP_IN_PRE, TAP_IN_DEC}) {
                    try {
                        if (ch.pipeline().get(n) != null) ch.pipeline().remove(n);
                    } catch (Throwable ignored) {
                    }
                }
            });
        }
        conns.clear();
    }

    Map<String, Integer> pendingQueues() {
        Map<String, Integer> m = new java.util.TreeMap<>();
        for (Conn c : conns.values()) {
            m.put(c.name + ".out", c.outQ.size());
            m.put(c.name + ".in", c.inQ.size());
        }
        return m;
    }

    // ------------------------------------------------------------------------------------------------
    // naming
    // ------------------------------------------------------------------------------------------------

    private static final ConcurrentHashMap<Long, String> NAMES = new ConcurrentHashMap<>();

    static String name(PacketSide side, ConnectionState state, ClientVersion v, int id) {
        if (v == null) v = ClientVersion.getLatest();
        final ClientVersion ver = v;
        long key = ((long) side.ordinal() << 52) | ((long) state.ordinal() << 44) | ((long) ver.ordinal() << 20) | (id & 0xFFFFF);
        return NAMES.computeIfAbsent(key, k -> {
            PacketTypeCommon t = null;
            try {
                t = PacketType.getById(side, state, ver, id);
            } catch (Throwable ignored) {
            }
            return state.name().toLowerCase(Locale.ROOT) + ":" + (t != null ? t.getName() : String.format("0x%02x", id));
        });
    }

    private static PacketTypeCommon type(PacketSide side, ConnectionState state, ClientVersion v, int id) {
        try {
            return PacketType.getById(side, state, v == null ? ClientVersion.getLatest() : v, id);
        } catch (Throwable t) {
            return null;
        }
    }

    // ------------------------------------------------------------------------------------------------
    // handlers
    // ------------------------------------------------------------------------------------------------

    private final class OutWire extends ChannelOutboundHandlerAdapter {
        private final Conn c;

        OutWire(Conn c) {
            this.c = c;
        }

        @Override
        public void write(ChannelHandlerContext ctx, Object msg, ChannelPromise promise) throws Exception {
            try {
                if (msg instanceof ByteBuf b && b.isReadable() && probe.enabled) {
                    boolean compressed = ctx.pipeline().get("compress") != null;
                    c.outParser.feed(b, compressed, (id, crc, len) -> onOutFrame(c, id, crc, len));
                }
            } catch (Throwable t) {
                probe.warn("out-wire", t);
                c.outParser.reset();
            }
            ctx.write(msg, promise);
        }
    }

    private void onOutFrame(Conn c, int id, long crc, int len) {
        String n = name(PacketSide.SERVER, c.outState, c.clientVersion, id);
        stats.inc("wire.out", n);
        stats.add("wire.out.bytes", n, len);
        stats.inc("player.wire.out." + c.who(), n);
        PacketTypeCommon t = type(PacketSide.SERVER, c.outState, c.clientVersion, id);
        if (t == PacketType.Play.Server.DISCONNECT || t == PacketType.Configuration.Server.DISCONNECT || t == PacketType.Login.Server.DISCONNECT) {
            stats.inc("wire.out.disconnect_packets", c.who());
        }
        // state machine of what the client will decode next
        if (c.outState == ConnectionState.LOGIN && t == PacketType.Login.Server.LOGIN_SUCCESS) c.outState = ConnectionState.CONFIGURATION;
        else if (c.outState == ConnectionState.CONFIGURATION && t == PacketType.Configuration.Server.CONFIGURATION_END) c.outState = ConnectionState.PLAY;
        else if (c.outState == ConnectionState.PLAY && t == PacketType.Play.Server.CONFIGURATION_START) c.outState = ConnectionState.CONFIGURATION;

        if (!c.outEncSeen) return; // in-flight accounting starts once the encoder tap saw a packet
        long[] head = c.outQ.peekFirst();
        if (head == null) {
            stats.inc("inflight.out.injected", n);
            return;
        }
        if (head[1] == crc) {
            c.outQ.pollFirst();
            stats.inc("inflight.out.matched", n);
            return;
        }
        int k = indexOfCrc(c.outQ, crc);
        if (k > 0) {
            for (int i = 0; i < k; i++) {
                long[] d = c.outQ.pollFirst();
                stats.inc("inflight.out.dropped", name(PacketSide.SERVER, ConnectionState.PLAY, c.serverProtocolVersion, (int) d[0]));
            }
            c.outQ.pollFirst();
            stats.inc("inflight.out.matched", n);
        } else if (head[0] == id) {
            c.outQ.pollFirst();
            stats.inc("inflight.out.modified", n);
        } else {
            stats.inc("inflight.out.injected", n);
        }
    }

    private static int indexOfCrc(ArrayDeque<long[]> q, long crc) {
        int i = 0;
        for (Iterator<long[]> it = q.iterator(); it.hasNext() && i < SEARCH_AHEAD; i++) {
            if (it.next()[1] == crc) return i;
        }
        return -1;
    }

    /** Sits directly after the vanilla encoder: sees the server-version bytes of every packet [id][body]. */
    private final class OutEnc extends ChannelOutboundHandlerAdapter {
        private final Conn c;
        private final CRC32 crc = new CRC32();

        OutEnc(Conn c) {
            this.c = c;
        }

        @Override
        public void write(ChannelHandlerContext ctx, Object msg, ChannelPromise promise) throws Exception {
            if (!(msg instanceof ByteBuf b) || !b.isReadable() || !probe.enabled) {
                ctx.write(msg, promise);
                return;
            }
            long t0;
            try {
                int id = FrameParser.peekVarInt(b, b.readerIndex());
                crc.reset();
                crc.update(b.nioBuffer(b.readerIndex(), b.readableBytes()));
                c.outQ.addLast(new long[]{id, crc.getValue()});
                if (c.outQ.size() > MAX_QUEUE) {
                    c.outQ.pollFirst();
                    stats.inc("inflight.out.queue_overflow", c.who());
                }
                c.outEncSeen = true;
            } catch (Throwable t) {
                probe.warn("out-enc", t);
            }
            t0 = System.nanoTime();
            try {
                ctx.write(msg, promise);
            } finally {
                stats.hist("cost.out.after_encoder_ns").record(System.nanoTime() - t0);
            }
        }
    }

    private final class InWire extends ChannelInboundHandlerAdapter {
        private final Conn c;

        InWire(Conn c) {
            this.c = c;
        }

        @Override
        public void channelRead(ChannelHandlerContext ctx, Object msg) throws Exception {
            try {
                if (msg instanceof ByteBuf b && b.isReadable() && probe.enabled) {
                    boolean compressed = ctx.pipeline().get("decompress") != null;
                    c.inParser.feed(b, compressed, (id, crc, len) -> onInFrame(c, id, crc, len));
                }
            } catch (Throwable t) {
                probe.warn("in-wire", t);
                c.inParser.reset();
            }
            ctx.fireChannelRead(msg);
        }
    }

    private void onInFrame(Conn c, int id, long crc, int len) {
        String n = name(PacketSide.CLIENT, c.inState, c.clientVersion, id);
        stats.inc("wire.in", n);
        stats.add("wire.in.bytes", n, len);
        stats.inc("player.wire.in." + c.who(), n);
        PacketTypeCommon t = type(PacketSide.CLIENT, c.inState, c.clientVersion, id);
        if (c.inState == ConnectionState.LOGIN && t == PacketType.Login.Client.LOGIN_SUCCESS_ACK) c.inState = ConnectionState.CONFIGURATION;
        else if (c.inState == ConnectionState.CONFIGURATION && t == PacketType.Configuration.Client.CONFIGURATION_END_ACK) c.inState = ConnectionState.PLAY;
        else if (c.inState == ConnectionState.PLAY && t == PacketType.Play.Client.CONFIGURATION_ACK) c.inState = ConnectionState.CONFIGURATION;
        if (c.relativeAttached) {
            c.inQ.addLast(new long[]{id, crc});
            if (c.inQ.size() > MAX_QUEUE) {
                c.inQ.pollFirst();
                stats.inc("inflight.in.queue_overflow", c.who());
            }
        }
    }

    /** After the splitter/decompressor: start of the decoder chain (PacketEvents/Via decoders). */
    private final class InPre extends ChannelInboundHandlerAdapter {
        private final Conn c;

        InPre(Conn c) {
            this.c = c;
        }

        @Override
        public void channelRead(ChannelHandlerContext ctx, Object msg) throws Exception {
            c.inT0 = System.nanoTime();
            ctx.fireChannelRead(msg);
        }
    }

    /** Directly before the vanilla decoder: sees the bytes the server will decode. */
    private final class InDec extends ChannelInboundHandlerAdapter {
        private final Conn c;
        private final CRC32 crc = new CRC32();

        InDec(Conn c) {
            this.c = c;
        }

        @Override
        public void channelRead(ChannelHandlerContext ctx, Object msg) throws Exception {
            try {
                if (msg instanceof ByteBuf b && b.isReadable() && probe.enabled) {
                    if (c.inT0 != 0) {
                        stats.hist("cost.in.decoder_chain_ns").record(System.nanoTime() - c.inT0);
                        c.inT0 = 0;
                    }
                    int id = FrameParser.peekVarInt(b, b.readerIndex());
                    crc.reset();
                    crc.update(b.nioBuffer(b.readerIndex(), b.readableBytes()));
                    long v = crc.getValue();
                    String n = name(PacketSide.CLIENT, c.inState, c.serverProtocolVersion, id);
                    long[] head = c.inQ.peekFirst();
                    if (head == null) {
                        stats.inc("inflight.in.injected", n);
                    } else if (head[1] == v) {
                        c.inQ.pollFirst();
                        stats.inc("inflight.in.matched", n);
                    } else {
                        int k = indexOfCrc(c.inQ, v);
                        if (k > 0) {
                            for (int i = 0; i < k; i++) {
                                long[] d = c.inQ.pollFirst();
                                stats.inc("inflight.in.dropped", name(PacketSide.CLIENT, c.inState, c.clientVersion, (int) d[0]));
                            }
                            c.inQ.pollFirst();
                            stats.inc("inflight.in.matched", n);
                        } else if (head[0] == id) {
                            c.inQ.pollFirst();
                            stats.inc("inflight.in.modified", n);
                        } else {
                            stats.inc("inflight.in.injected", n);
                        }
                    }
                }
            } catch (Throwable t) {
                probe.warn("in-dec", t);
            }
            ctx.fireChannelRead(msg);
        }
    }

    // ------------------------------------------------------------------------------------------------
    // frame parser
    // ------------------------------------------------------------------------------------------------

    interface FrameSink {
        void frame(int packetId, long crc, int wireLen);
    }

    /** Streaming parser for [VarInt len][payload] frames; payload = [VarInt dataLen][zlib|raw] when compressed. */
    static final class FrameParser {
        private byte[] buf = new byte[8192];
        private int len;
        private final Inflater inflater = new Inflater();
        private final CRC32 crc = new CRC32();
        private byte[] out = new byte[8192];

        void reset() {
            len = 0;
        }

        void feed(ByteBuf in, boolean compressed, FrameSink sink) throws DataFormatException {
            int n = in.readableBytes();
            if (len + n > buf.length) {
                byte[] nb = new byte[Math.max(buf.length * 2, len + n)];
                System.arraycopy(buf, 0, nb, 0, len);
                buf = nb;
            }
            in.getBytes(in.readerIndex(), buf, len, n);
            len += n;
            if (len > (1 << 24)) { // never expected: a corrupt stream would otherwise grow the buffer forever
                len = 0;
                return;
            }
            int pos = 0;
            while (true) {
                long lv = readVarInt(buf, pos, len);
                if (lv < 0) break;
                int frameLen = (int) (lv >>> 8);
                int vl = (int) (lv & 0xFF);
                if (frameLen < 0 || frameLen > (1 << 23)) { // corrupt (should not happen): resync by dropping buffer
                    len = 0;
                    return;
                }
                if (pos + vl + frameLen > len) break;
                int start = pos + vl;
                handleFrame(start, frameLen, compressed, sink);
                pos = start + frameLen;
            }
            if (pos > 0) {
                System.arraycopy(buf, pos, buf, 0, len - pos);
                len -= pos;
            }
        }

        private void handleFrame(int start, int frameLen, boolean compressed, FrameSink sink) throws DataFormatException {
            byte[] data = buf;
            int off = start;
            int dlen = frameLen;
            if (compressed) {
                long dv = readVarInt(buf, start, start + frameLen);
                if (dv < 0) return;
                int dataLen = (int) (dv >>> 8);
                int vl = (int) (dv & 0xFF);
                if (dataLen == 0) {
                    off = start + vl;
                    dlen = frameLen - vl;
                } else {
                    if (out.length < dataLen) out = new byte[Math.max(dataLen, out.length * 2)];
                    inflater.reset();
                    inflater.setInput(buf, start + vl, frameLen - vl);
                    int got = 0;
                    while (got < dataLen && !inflater.finished()) {
                        int r = inflater.inflate(out, got, dataLen - got);
                        if (r == 0 && (inflater.needsInput() || inflater.needsDictionary())) break;
                        got += r;
                    }
                    data = out;
                    off = 0;
                    dlen = got;
                }
            }
            if (dlen <= 0) return;
            long iv = readVarInt(data, off, off + dlen);
            if (iv < 0) return;
            crc.reset();
            crc.update(data, off, dlen);
            sink.frame((int) (iv >>> 8), crc.getValue(), frameLen);
        }

        /** @return (value << 8) | varIntLength, or -1 when incomplete. */
        static long readVarInt(byte[] b, int pos, int limit) {
            int value = 0;
            for (int i = 0; i < 5; i++) {
                if (pos + i >= limit) return -1;
                byte k = b[pos + i];
                value |= (k & 0x7F) << (7 * i);
                if ((k & 0x80) == 0) return ((long) value << 8) | (i + 1);
            }
            return -1;
        }

        static int peekVarInt(ByteBuf b, int idx) {
            int value = 0;
            for (int i = 0; i < 5 && idx + i < b.writerIndex(); i++) {
                byte k = b.getByte(idx + i);
                value |= (k & 0x7F) << (7 * i);
                if ((k & 0x80) == 0) return value;
            }
            return -1;
        }
    }
}
