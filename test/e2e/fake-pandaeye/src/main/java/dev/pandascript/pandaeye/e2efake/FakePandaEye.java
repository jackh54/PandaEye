package dev.pandascript.pandaeye.e2efake;

import com.github.retrooper.packetevents.PacketEvents;
import com.github.retrooper.packetevents.event.PacketListener;
import com.github.retrooper.packetevents.event.PacketListenerCommon;
import com.github.retrooper.packetevents.event.PacketListenerPriority;
import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientClickWindow;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientInteractEntity;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerAbilities;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerFlying;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityVelocity;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPing;
import io.papermc.paper.threadedregions.scheduler.ScheduledTask;
import org.bukkit.Bukkit;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.plugin.java.JavaPlugin;
import org.jetbrains.annotations.NotNull;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.LongAdder;

/**
 * Fake "PandaEye" (test double, NOT the product). It behaves like a shadow-mode citizen: one MONITOR listener,
 * wrapper hygiene, a bounded flag writer thread "PandaEye-Log", a "PandaEye-Cloud" thread that tries to reach the
 * configured endpoint, a bounded onDisable. A few trivial local checks write plugins/PandaEye/logs/flags-*.jsonl so
 * the detection plumbing can be exercised.
 *
 * config.yml {@code e2e-fake.violation} turns it into a negative control for the harness:
 *   none (default) | ping (sends PINGs) | wrap (leaves a wrapper attached -> re-encode) | cancel (cancels packets)
 *   | leak-thread (non-daemon thread survives unload) | slow-disable (onDisable sleeps 800 ms)
 */
public final class FakePandaEye extends JavaPlugin {
    private volatile boolean active;
    private PacketListenerCommon listener;
    private String violation = "none";
    private Thread cloudThread;
    private Thread logThread;
    private final BlockingQueue<String> logQ = new ArrayBlockingQueue<>(10_000);
    private final LongAdder packets = new LongAdder();
    private final LongAdder nanos = new LongAdder();
    private final AtomicLong maxNs = new AtomicLong();
    private final AtomicLong inFlight = new AtomicLong();
    private volatile String cloudState = "DISCONNECTED";
    private volatile Socket socket; // closed on disable: interrupt() does not unblock a socket read
    private ScheduledTask pinger;

    @Override
    public void onEnable() {
        saveDefaultConfig();
        violation = getConfig().getString("e2e-fake.violation", "none").toLowerCase(Locale.ROOT);
        active = true;
        listener = PacketEvents.getAPI().getEventManager().registerListener(new Listener(), PacketListenerPriority.MONITOR);
        startThreads(getConfig().getString("cloud.endpoint", "ws://127.0.0.1:8080/v1/connect"));
        if (violation.equals("ping")) {
            pinger = Bukkit.getGlobalRegionScheduler().runAtFixedRate(this, t -> {
                for (var p : Bukkit.getOnlinePlayers()) {
                    User u = PacketEvents.getAPI().getPlayerManager().getUser(p);
                    if (u != null) u.sendPacket(new WrapperPlayServerPing(424242));
                }
            }, 40, 40);
        }
        if (violation.equals("leak-thread")) {
            Thread t = new Thread(() -> {
                while (true) try { Thread.sleep(1000); } catch (InterruptedException ignored) { }
            }, "PandaEye-Leak");
            t.setDaemon(false);
            t.start();
        }
        getLogger().info("PandaEye e2e FAKE enabled (mode shadow, violation=" + violation + ")");
    }

    @Override
    public void onDisable() {
        long t0 = System.nanoTime();
        if (listener != null) PacketEvents.getAPI().getEventManager().unregisterListener(listener);
        active = false;
        for (int i = 0; i < 100 && inFlight.get() > 0; i++) sleep(1);
        if (pinger != null) pinger.cancel();
        Bukkit.getGlobalRegionScheduler().cancelTasks(this);
        Bukkit.getAsyncScheduler().cancelTasks(this);
        if (cloudThread != null) cloudThread.interrupt();
        Socket s = socket;
        if (s != null) try { s.close(); } catch (IOException ignored) { }
        if (logThread != null) logThread.interrupt();
        join(cloudThread, 100);
        join(logThread, 100);
        if (violation.equals("slow-disable")) sleep(800);
        getLogger().info("PandaEye e2e FAKE disabled in " + (System.nanoTime() - t0) / 1_000_000 + " ms");
    }

    private static void sleep(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }

    private static void join(Thread t, long ms) {
        if (t == null) return;
        try { t.join(ms); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }

    private void startThreads(String endpoint) {
        cloudThread = new Thread(() -> {
            long backoff = 1000;
            while (active && !Thread.currentThread().isInterrupted()) {
                try {
                    URI u = URI.create(endpoint);
                    try (Socket s = new Socket()) {
                        socket = s;
                        s.connect(new InetSocketAddress(u.getHost(), u.getPort() < 0 ? 80 : u.getPort()), 5000);
                        s.setSoTimeout(5000);
                        cloudState = "TCP_CONNECTED";
                        s.getInputStream().read(); // black hole: blocks until timeout
                    }
                } catch (IOException e) {
                    cloudState = "DISCONNECTED";
                }
                try { Thread.sleep(backoff); } catch (InterruptedException e) { return; }
                backoff = Math.min(60_000, backoff * 2);
            }
        }, "PandaEye-Cloud");
        cloudThread.setDaemon(true);
        cloudThread.start();
        logThread = new Thread(() -> {
            Path dir = getDataFolder().toPath().resolve("logs");
            try {
                Files.createDirectories(dir);
                while (!Thread.currentThread().isInterrupted()) {
                    String line = logQ.poll(200, TimeUnit.MILLISECONDS);
                    if (line == null) continue;
                    List<String> batch = new ArrayList<>();
                    batch.add(line);
                    logQ.drainTo(batch, 1000);
                    Files.write(dir.resolve("flags-" + LocalDate.now() + ".jsonl"), batch, StandardCharsets.UTF_8,
                            StandardOpenOption.CREATE, StandardOpenOption.APPEND);
                }
            } catch (InterruptedException ignored) {
            } catch (IOException e) {
                getLogger().warning("flag log: " + e);
            }
        }, "PandaEye-Log");
        logThread.setDaemon(true);
        logThread.start();
    }

    private void flag(User u, String check, String info) {
        String json = String.format(Locale.ROOT, "{\"time\":%d,\"uuid\":\"%s\",\"name\":\"%s\",\"check\":\"%s\",\"confirmed\":true,\"count\":1,\"info\":\"%s\",\"fake\":true}",
                System.currentTimeMillis(), u.getUUID(), u.getName(), check, info);
        logQ.offer(json); // bounded: drops when full
    }

    /** Wrapper hygiene (ARCHITECTURE 4.2). */
    private static <W extends PacketWrapper<?>> W read(PacketReceiveEvent e, java.util.function.Function<PacketReceiveEvent, W> f) {
        PacketWrapper<?> prev = e.getLastUsedWrapper();
        Object buf = e.getByteBuf();
        int ri = buf instanceof io.netty.buffer.ByteBuf b ? b.readerIndex() : -1;
        try { return f.apply(e); } finally {
            e.setLastUsedWrapper(prev);
            if (ri >= 0) ((io.netty.buffer.ByteBuf) buf).readerIndex(ri);
        }
    }

    private final class Listener implements PacketListener {
        private int sendCounter;

        @Override
        public void onPacketReceive(PacketReceiveEvent e) {
            if (!active) return;
            inFlight.incrementAndGet();
            long t0 = System.nanoTime();
            try {
                var t = e.getPacketType();
                if (t == PacketType.Play.Client.PLAYER_ROTATION || t == PacketType.Play.Client.PLAYER_POSITION_AND_ROTATION) {
                    WrapperPlayClientPlayerFlying w = read(e, WrapperPlayClientPlayerFlying::new);
                    if (Math.abs(w.getLocation().getPitch()) > 90.001) flag(e.getUser(), "protocol.pitch", "pitch " + w.getLocation().getPitch());
                } else if (t == PacketType.Play.Client.PLAYER_ABILITIES) {
                    if (read(e, WrapperPlayClientPlayerAbilities::new).isFlying()) flag(e.getUser(), "protocol.abilities", "flying");
                } else if (t == PacketType.Play.Client.CLICK_WINDOW) {
                    int id = read(e, WrapperPlayClientClickWindow::new).getWindowId();
                    if (id > 1 && id == 37) flag(e.getUser(), "inventory.closed", "window " + id);
                } else if (t == PacketType.Play.Client.INTERACT_ENTITY) {
                    if (read(e, WrapperPlayClientInteractEntity::new).getEntityId() == e.getUser().getEntityId()) flag(e.getUser(), "protocol.selfhit", "self");
                }
                if (violation.equals("cancel") && t == PacketType.Play.Client.ANIMATION && (++sendCounter % 10 == 0)) e.setCancelled(true);
            } catch (Throwable ignored) {
            } finally {
                record(System.nanoTime() - t0);
                inFlight.decrementAndGet();
            }
        }

        @Override
        public void onPacketSend(PacketSendEvent e) {
            if (!active) return;
            inFlight.incrementAndGet();
            long t0 = System.nanoTime();
            try {
                if (violation.equals("wrap") && e.getPacketType() == PacketType.Play.Server.ENTITY_VELOCITY) {
                    new WrapperPlayServerEntityVelocity(e); // BAD: wrapper left attached -> re-encode with reEncodeByDefault
                }
            } catch (Throwable ignored) {
            } finally {
                record(System.nanoTime() - t0);
                inFlight.decrementAndGet();
            }
        }
    }

    private void record(long ns) {
        packets.increment();
        nanos.add(ns);
        maxNs.accumulateAndGet(ns, Math::max);
    }

    @Override
    public boolean onCommand(@NotNull CommandSender sender, @NotNull Command command, @NotNull String label, String @NotNull [] args) {
        if (args.length == 0 || args[0].equalsIgnoreCase("status")) {
            long n = Math.max(1, packets.sum());
            sender.sendMessage("PandaEye (e2e fake) status: mode: shadow, enforcement: false, cloud: " + cloudState
                    + ", packet handler: avg " + String.format(Locale.ROOT, "%.2f", nanos.sum() / 1000.0 / n) + " us p99 "
                    + String.format(Locale.ROOT, "%.2f", maxNs.get() / 1000.0) + " us/packet (p99 approximated by max), packets " + packets.sum()
                    + ", enforcer attempts 0, violation mode " + violation);
            return true;
        }
        sender.sendMessage("usage: /pandaeye status");
        return true;
    }
}
