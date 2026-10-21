package dev.pandascript.pandaeye.e2e.probe;

import com.github.retrooper.packetevents.PacketEvents;
import com.github.retrooper.packetevents.event.PacketListener;
import com.github.retrooper.packetevents.event.PacketListenerCommon;
import com.github.retrooper.packetevents.event.PacketListenerPriority;
import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.event.ProtocolPacketEvent;
import com.github.retrooper.packetevents.event.UserDisconnectEvent;
import com.github.retrooper.packetevents.event.UserLoginEvent;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.packettype.PacketTypeCommon;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.util.Vector3d;
import com.github.retrooper.packetevents.util.Vector3f;
import com.github.retrooper.packetevents.util.Vector3i;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientAnimation;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientClickWindow;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientEntityAction;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientHeldItemChange;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientInteractEntity;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientKeepAlive;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerAbilities;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerBlockPlacement;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerDigging;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerFlying;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPong;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientUseItem;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityVelocity;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerKeepAlive;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPing;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerPositionAndLook;
import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import io.netty.buffer.ByteBuf;

import java.util.ArrayDeque;
import java.util.Iterator;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Function;
import java.util.zip.CRC32;

/**
 * PacketEvents listeners at LOWEST and MONITOR. LOWEST snapshots (cancelled, lastUsedWrapper, needsReEncode,
 * buffer indices, CRC of the readable bytes); MONITOR compares, so anything another listener did to the event in
 * between is counted per packet type. The MONITOR listener is re-registered after every plugin enable so it runs
 * after every other MONITOR listener (PacketEvents keeps insertion order within a priority).
 *
 * The probe never creates a wrapper except for players that are being traced ({@code probe trace}), and then with
 * the hygiene pattern of ARCHITECTURE 4.2 (lastUsedWrapper and reader index restored).
 */
final class PeProbe {
    private final ProbePlugin probe;
    private final Stats stats;
    private PacketListenerCommon lowest;
    private volatile PacketListenerCommon monitor;
    private volatile int monitorGeneration;
    volatile boolean hashBuffers = true;
    final ConcurrentHashMap<String, Trace> traces = new ConcurrentHashMap<>();

    PeProbe(ProbePlugin probe, Stats stats) {
        this.probe = probe;
        this.stats = stats;
    }

    private static final class Snap {
        ProtocolPacketEvent event;
        boolean cancelled;
        PacketWrapper<?> wrapper;
        boolean reencode;
        int ri, wi;
        long crc;
    }

    private final ThreadLocal<ArrayDeque<Snap>> stack = ThreadLocal.withInitial(ArrayDeque::new);
    private final ThreadLocal<CRC32> crcs = ThreadLocal.withInitial(CRC32::new);

    void register() {
        lowest = PacketEvents.getAPI().getEventManager().registerListener(new Lowest(), PacketListenerPriority.LOWEST);
        reRegisterMonitor();
    }

    /** Make the MONITOR listener the last registered one (it then runs after every other MONITOR listener). */
    synchronized void reRegisterMonitor() {
        int gen = ++monitorGeneration;
        PacketListenerCommon old = monitor;
        monitor = PacketEvents.getAPI().getEventManager().registerListener(new Monitor(gen), PacketListenerPriority.MONITOR);
        if (old != null) PacketEvents.getAPI().getEventManager().unregisterListener(old);
    }

    void unregister() {
        try {
            if (lowest != null) PacketEvents.getAPI().getEventManager().unregisterListener(lowest);
            if (monitor != null) PacketEvents.getAPI().getEventManager().unregisterListener(monitor);
        } catch (Throwable ignored) {
        }
    }

    private static String key(ProtocolPacketEvent e) {
        PacketTypeCommon t = e.getPacketType();
        String state;
        try {
            state = e.getConnectionState().name().toLowerCase(Locale.ROOT);
        } catch (Throwable ex) {
            state = "?";
        }
        return state + ":" + (t == null ? "id_" + e.getPacketId() : t.getName());
    }

    private long crcOf(ProtocolPacketEvent e) {
        if (!hashBuffers) return 0;
        Object o = e.getByteBuf();
        if (!(o instanceof ByteBuf b)) return 0;
        CRC32 c = crcs.get();
        c.reset();
        if (b.readableBytes() > 0) c.update(b.nioBuffer(b.readerIndex(), b.readableBytes()));
        return c.getValue();
    }

    private void lowest(ProtocolPacketEvent e, String dir) {
        if (!probe.enabled) return;
        try {
            String k = key(e);
            stats.inc("pe." + dir + ".lowest", k);
            Snap s = new Snap();
            s.event = e;
            s.cancelled = e.isCancelled();
            s.wrapper = e.getLastUsedWrapper();
            s.reencode = e.needsReEncode();
            Object o = e.getByteBuf();
            if (o instanceof ByteBuf b) {
                s.ri = b.readerIndex();
                s.wi = b.writerIndex();
            }
            s.crc = crcOf(e);
            if (s.cancelled) stats.inc("pe." + dir + ".cancelled_at_lowest", k);
            if (s.wrapper != null) stats.inc("pe." + dir + ".wrapper_at_lowest", k);
            if (s.reencode) stats.inc("pe." + dir + ".reencode_flag_at_lowest", k);
            ArrayDeque<Snap> st = stack.get();
            st.push(s);
            if (st.size() > 64) st.removeLast();
            Trace tr = traces.get(nameOf(e.getUser()));
            if (tr != null) trace(tr, e, dir);
        } catch (Throwable t) {
            probe.warn("pe-lowest", t);
        }
    }

    private void monitor(ProtocolPacketEvent e, String dir, int gen) {
        if (!probe.enabled || gen != monitorGeneration) return;
        try {
            String k = key(e);
            stats.inc("pe." + dir + ".monitor", k);
            ArrayDeque<Snap> st = stack.get();
            Snap s = null;
            for (Iterator<Snap> it = st.iterator(); it.hasNext(); ) {
                Snap c = it.next();
                if (c.event == e) {
                    s = c;
                    it.remove();
                    break;
                }
            }
            if (s == null) {
                stats.inc("pe." + dir + ".no_lowest_snapshot", k);
                return;
            }
            boolean cancelled = e.isCancelled();
            if (cancelled) stats.inc("pe." + dir + ".cancelled_at_monitor", k);
            if (cancelled != s.cancelled) stats.inc("pe." + dir + ".cancel_changed", k);
            PacketWrapper<?> w = e.getLastUsedWrapper();
            if (w != null) stats.inc("pe." + dir + ".wrapper_at_monitor", k);
            if (w != s.wrapper) stats.inc("pe." + dir + ".wrapper_changed", k);
            boolean re = e.needsReEncode();
            if (re != s.reencode) stats.inc("pe." + dir + ".reencode_flag_changed", k);
            // what PacketEvents will do after the listeners: re-encode when a wrapper is attached and the flag is set
            if (w != null && re) stats.inc("pe." + dir + ".will_reencode", k);
            Object o = e.getByteBuf();
            if (o instanceof ByteBuf b) {
                if (b.readerIndex() != s.ri || b.writerIndex() != s.wi) stats.inc("pe." + dir + ".buffer_index_changed", k);
            }
            if (hashBuffers && crcOf(e) != s.crc) stats.inc("pe." + dir + ".buffer_bytes_changed", k);
        } catch (Throwable t) {
            probe.warn("pe-monitor", t);
        }
    }

    private final class Lowest implements PacketListener {
        @Override
        public void onPacketReceive(PacketReceiveEvent e) {
            lowest(e, "in");
        }

        @Override
        public void onPacketSend(PacketSendEvent e) {
            lowest(e, "out");
        }

        @Override
        public void onUserLogin(UserLoginEvent e) {
            if (probe.enabled) probe.taps.attachWire(e.getUser());
        }

        @Override
        public void onUserDisconnect(UserDisconnectEvent e) {
            if (probe.enabled) stats.inc("pe.user_disconnect", nameOf(e.getUser()));
        }
    }

    private final class Monitor implements PacketListener {
        private final int gen;

        Monitor(int gen) {
            this.gen = gen;
        }

        @Override
        public void onPacketReceive(PacketReceiveEvent e) {
            monitor(e, "in", gen);
        }

        @Override
        public void onPacketSend(PacketSendEvent e) {
            monitor(e, "out", gen);
        }
    }

    static String nameOf(User u) {
        if (u == null) return "?";
        try {
            String n = u.getName();
            return n == null ? "?" : n.toLowerCase(Locale.ROOT);
        } catch (Throwable t) {
            return "?";
        }
    }

    // ------------------------------------------------------------------------------------------------
    // tracing (cheat-client verification): decoded fields of selected packets for selected players
    // ------------------------------------------------------------------------------------------------

    static final class Trace {
        final long startNs = System.nanoTime();
        final long endNs;
        final int max;
        final ArrayDeque<JsonObject> entries = new ArrayDeque<>();
        int dropped;

        Trace(int seconds, int max) {
            this.endNs = startNs + seconds * 1_000_000_000L;
            this.max = max;
        }

        synchronized void add(JsonObject o) {
            if (entries.size() >= max) {
                dropped++;
                return;
            }
            entries.add(o);
        }

        synchronized JsonObject json() {
            JsonObject o = new JsonObject();
            JsonArray a = new JsonArray();
            for (JsonObject j : entries) a.add(j);
            o.add("entries", a);
            o.addProperty("dropped", dropped);
            return o;
        }
    }

    private static <E extends ProtocolPacketEvent, W extends PacketWrapper<?>> W wrap(E e, Function<E, W> f) {
        PacketWrapper<?> prev = e.getLastUsedWrapper();
        Object o = e.getByteBuf();
        int ri = o instanceof ByteBuf b ? b.readerIndex() : -1;
        try {
            return f.apply(e);
        } finally {
            e.setLastUsedWrapper(prev);
            if (ri >= 0) ((ByteBuf) o).readerIndex(ri);
        }
    }

    private void trace(Trace tr, ProtocolPacketEvent e, String dir) {
        long now = System.nanoTime();
        if (now > tr.endNs) return;
        PacketTypeCommon t = e.getPacketType();
        JsonObject j = new JsonObject();
        j.addProperty("t", (now - tr.startNs) / 1e6);
        j.addProperty("d", dir);
        j.addProperty("p", t == null ? "id_" + e.getPacketId() : t.getName());
        if (e.isCancelled()) j.addProperty("cancelled", true);
        try {
            if (e instanceof PacketReceiveEvent re) traceIn(re, t, j);
            else if (e instanceof PacketSendEvent se && !traceOut(se, t, j)) return;
        } catch (Throwable ex) {
            j.addProperty("decode_error", String.valueOf(ex));
        }
        tr.add(j);
    }

    private static void traceIn(PacketReceiveEvent e, PacketTypeCommon t, JsonObject j) {
        if (t == PacketType.Play.Client.PLAYER_FLYING || t == PacketType.Play.Client.PLAYER_POSITION
                || t == PacketType.Play.Client.PLAYER_POSITION_AND_ROTATION || t == PacketType.Play.Client.PLAYER_ROTATION) {
            WrapperPlayClientPlayerFlying w = wrap(e, WrapperPlayClientPlayerFlying::new);
            if (w.hasPositionChanged()) {
                j.addProperty("x", w.getLocation().getX());
                j.addProperty("y", w.getLocation().getY());
                j.addProperty("z", w.getLocation().getZ());
            }
            if (w.hasRotationChanged()) {
                j.addProperty("yaw", w.getLocation().getYaw());
                j.addProperty("pitch", w.getLocation().getPitch());
            }
            j.addProperty("g", w.isOnGround());
            if (w.isHorizontalCollision()) j.addProperty("hc", true);
        } else if (t == PacketType.Play.Client.INTERACT_ENTITY) {
            WrapperPlayClientInteractEntity w = wrap(e, WrapperPlayClientInteractEntity::new);
            j.addProperty("eid", w.getEntityId());
            j.addProperty("action", String.valueOf(w.getAction()));
        } else if (t == PacketType.Play.Client.ANIMATION) {
            j.addProperty("hand", String.valueOf(wrap(e, WrapperPlayClientAnimation::new).getHand()));
        } else if (t == PacketType.Play.Client.PLAYER_BLOCK_PLACEMENT) {
            WrapperPlayClientPlayerBlockPlacement w = wrap(e, WrapperPlayClientPlayerBlockPlacement::new);
            Vector3i b = w.getBlockPosition();
            Vector3f c = w.getCursorPosition();
            j.addProperty("bx", b.getX());
            j.addProperty("by", b.getY());
            j.addProperty("bz", b.getZ());
            j.addProperty("face", String.valueOf(w.getFace()));
            if (c != null) {
                j.addProperty("cx", c.getX());
                j.addProperty("cy", c.getY());
                j.addProperty("cz", c.getZ());
            }
        } else if (t == PacketType.Play.Client.PLAYER_DIGGING) {
            WrapperPlayClientPlayerDigging w = wrap(e, WrapperPlayClientPlayerDigging::new);
            Vector3i b = w.getBlockPosition();
            j.addProperty("action", String.valueOf(w.getAction()));
            j.addProperty("bx", b.getX());
            j.addProperty("by", b.getY());
            j.addProperty("bz", b.getZ());
            j.addProperty("face", String.valueOf(w.getBlockFace()));
        } else if (t == PacketType.Play.Client.CLICK_WINDOW) {
            WrapperPlayClientClickWindow w = wrap(e, WrapperPlayClientClickWindow::new);
            j.addProperty("window", w.getWindowId());
            j.addProperty("slot", w.getSlot());
            j.addProperty("button", w.getButton());
            j.addProperty("mode", String.valueOf(w.getWindowClickType()));
        } else if (t == PacketType.Play.Client.PLAYER_ABILITIES) {
            j.addProperty("flying", wrap(e, WrapperPlayClientPlayerAbilities::new).isFlying());
        } else if (t == PacketType.Play.Client.KEEP_ALIVE) {
            j.addProperty("id", wrap(e, WrapperPlayClientKeepAlive::new).getId());
        } else if (t == PacketType.Play.Client.PONG) {
            j.addProperty("id", wrap(e, WrapperPlayClientPong::new).getId());
        } else if (t == PacketType.Play.Client.ENTITY_ACTION) {
            j.addProperty("action", String.valueOf(wrap(e, WrapperPlayClientEntityAction::new).getAction()));
        } else if (t == PacketType.Play.Client.HELD_ITEM_CHANGE) {
            j.addProperty("slot", wrap(e, WrapperPlayClientHeldItemChange::new).getSlot());
        } else if (t == PacketType.Play.Client.USE_ITEM) {
            j.addProperty("hand", String.valueOf(wrap(e, WrapperPlayClientUseItem::new).getHand()));
        }
    }

    /** @return false when the packet is not traced */
    private static boolean traceOut(PacketSendEvent e, PacketTypeCommon t, JsonObject j) {
        if (t == PacketType.Play.Server.KEEP_ALIVE) {
            j.addProperty("id", wrap(e, WrapperPlayServerKeepAlive::new).getId());
        } else if (t == PacketType.Play.Server.PING) {
            j.addProperty("id", wrap(e, WrapperPlayServerPing::new).getId());
        } else if (t == PacketType.Play.Server.ENTITY_VELOCITY) {
            WrapperPlayServerEntityVelocity w = wrap(e, WrapperPlayServerEntityVelocity::new);
            if (w.getEntityId() != e.getUser().getEntityId()) return false; // only the player's own velocity
            Vector3d v = w.getVelocity();
            j.addProperty("vx", v.getX());
            j.addProperty("vy", v.getY());
            j.addProperty("vz", v.getZ());
        } else if (t == PacketType.Play.Server.PLAYER_POSITION_AND_LOOK) {
            WrapperPlayServerPlayerPositionAndLook w = wrap(e, WrapperPlayServerPlayerPositionAndLook::new);
            j.addProperty("x", w.getX());
            j.addProperty("y", w.getY());
            j.addProperty("z", w.getZ());
            j.addProperty("tid", w.getTeleportId());
        } else {
            return false; // other server packets are not traced (volume)
        }
        return true;
    }

    JsonObject tracesJson() {
        JsonObject o = new JsonObject();
        for (Map.Entry<String, Trace> e : traces.entrySet()) o.add(e.getKey(), e.getValue().json());
        return o;
    }
}
