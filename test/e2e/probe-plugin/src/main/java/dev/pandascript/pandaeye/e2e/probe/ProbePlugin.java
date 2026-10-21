package dev.pandascript.pandaeye.e2e.probe;

import com.github.retrooper.packetevents.PacketEvents;
import com.google.gson.GsonBuilder;
import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import net.kyori.adventure.text.serializer.plain.PlainTextComponentSerializer;
import org.bukkit.Bukkit;
import org.bukkit.GameMode;
import org.bukkit.Location;
import org.bukkit.Material;
import org.bukkit.NamespacedKey;
import org.bukkit.Registry;
import org.bukkit.World;
import org.bukkit.command.Command;
import org.bukkit.command.CommandSender;
import org.bukkit.entity.Entity;
import org.bukkit.entity.EntityType;
import org.bukkit.entity.Player;
import org.bukkit.event.EventHandler;
import org.bukkit.event.EventPriority;
import org.bukkit.event.Listener;
import org.bukkit.event.player.PlayerJoinEvent;
import org.bukkit.event.player.PlayerKickEvent;
import org.bukkit.event.player.PlayerQuitEvent;
import org.bukkit.event.server.PluginDisableEvent;
import org.bukkit.event.server.PluginEnableEvent;
import org.bukkit.inventory.ItemStack;
import org.bukkit.plugin.Plugin;
import org.bukkit.plugin.java.JavaPlugin;
import org.bukkit.potion.PotionEffect;
import org.bukkit.potion.PotionEffectType;
import org.bukkit.util.Vector;
import org.jetbrains.annotations.NotNull;

import java.io.IOException;
import java.lang.ref.WeakReference;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArraySet;
import java.util.concurrent.TimeUnit;

/**
 * PandaEye e2e probe. See test/e2e/README.md. Console commands: {@code probe help}.
 * Every command logs "[probe] OK <command>" (or "[probe] ERR ...") when it has finished, so the orchestrator can
 * synchronise on the log.
 */
public final class ProbePlugin extends JavaPlugin implements Listener {
    volatile boolean enabled = true;
    final Stats stats = new Stats();
    WireTaps taps;
    PeProbe pe;
    TickMonitor tick;
    private Arena arena;
    private final List<JsonObject> kicks = Collections.synchronizedList(new ArrayList<>());
    private final List<JsonObject> quits = Collections.synchronizedList(new ArrayList<>());
    private final List<JsonObject> joins = Collections.synchronizedList(new ArrayList<>());
    private final List<JsonObject> pluginEvents = Collections.synchronizedList(new ArrayList<>());
    private final Set<String> watchedNames = new CopyOnWriteArraySet<>(List.of("pandaeye"));
    private final List<Watched> watched = Collections.synchronizedList(new ArrayList<>());
    private final Map<String, Long> warnTimes = new ConcurrentHashMap<>();
    private volatile long resetAtMs = System.currentTimeMillis();
    private volatile io.papermc.paper.threadedregions.scheduler.ScheduledTask pingerTask;
    private final java.util.concurrent.atomic.AtomicInteger pingId = new java.util.concurrent.atomic.AtomicInteger(0x7E000000);

    private record Watched(String plugin, long enabledAtMs, WeakReference<ClassLoader> loader, String loaderName) {
    }

    @Override
    public void onEnable() {
        taps = new WireTaps(this, stats);
        pe = new PeProbe(this, stats);
        tick = new TickMonitor(this);
        arena = new Arena(this);
        pe.register();
        tick.start();
        getServer().getPluginManager().registerEvents(this, this);
        for (Player p : Bukkit.getOnlinePlayers()) onlineAttach(p);
        getLogger().info("[probe] enabled; PacketEvents " + PacketEvents.getAPI().getVersion()
                + " reEncodeByDefault=" + PacketEvents.getAPI().getSettings().reEncodeByDefault()
                + " folia=" + isFolia());
    }

    @Override
    public void onDisable() {
        enabled = false;
        if (pe != null) pe.unregister();
        if (tick != null) tick.stop();
        if (taps != null) taps.detachAll();
    }

    static boolean isFolia() {
        try {
            Class.forName("io.papermc.paper.threadedregions.RegionizedServer");
            return true;
        } catch (ClassNotFoundException e) {
            return false;
        }
    }

    Path serverDir() {
        return Path.of("").toAbsolutePath();
    }

    void warn(String site, Throwable t) {
        long now = System.currentTimeMillis();
        Long last = warnTimes.get(site);
        stats.inc("probe.errors", site);
        if (last != null && now - last < 10_000) return;
        warnTimes.put(site, now);
        getLogger().warning("[probe] error at " + site + ": " + t);
    }

    void debug(String msg) {
        getLogger().info("[probe] " + msg);
    }

    private void onlineAttach(Player p) {
        Object ch = PacketEvents.getAPI().getPlayerManager().getChannel(p);
        taps.attachRelative(ch, p.getName().toLowerCase(Locale.ROOT));
        tick.track(p);
    }

    // ------------------------------------------------------------------------------------------------
    // Bukkit listeners (MONITOR, read-only)
    // ------------------------------------------------------------------------------------------------

    @EventHandler(priority = EventPriority.MONITOR)
    public void onJoin(PlayerJoinEvent e) {
        if (!enabled) return;
        Player p = e.getPlayer();
        JsonObject o = new JsonObject();
        o.addProperty("t", System.currentTimeMillis());
        o.addProperty("name", p.getName());
        o.addProperty("entity_id", p.getEntityId());
        o.addProperty("uuid", p.getUniqueId().toString());
        bounded(joins, o);
        onlineAttach(p);
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onKick(PlayerKickEvent e) {
        if (!enabled) return;
        JsonObject o = new JsonObject();
        o.addProperty("t", System.currentTimeMillis());
        o.addProperty("name", e.getPlayer().getName());
        o.addProperty("reason", PlainTextComponentSerializer.plainText().serialize(e.reason()));
        o.addProperty("cause", String.valueOf(e.getCause()));
        o.addProperty("cancelled", e.isCancelled());
        bounded(kicks, o);
        getLogger().info("[probe] KICK " + e.getPlayer().getName() + " cause=" + e.getCause() + " reason=" + o.get("reason").getAsString());
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onQuit(PlayerQuitEvent e) {
        if (!enabled) return;
        JsonObject o = new JsonObject();
        o.addProperty("t", System.currentTimeMillis());
        o.addProperty("name", e.getPlayer().getName());
        o.addProperty("reason", String.valueOf(e.getReason()));
        bounded(quits, o);
        tick.untrack(e.getPlayer());
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onPluginEnable(PluginEnableEvent e) {
        if (e.getPlugin() == this) return;
        JsonObject o = new JsonObject();
        o.addProperty("t", System.currentTimeMillis());
        o.addProperty("event", "enable");
        o.addProperty("plugin", e.getPlugin().getName());
        bounded(pluginEvents, o);
        if (watchedNames.contains(e.getPlugin().getName().toLowerCase(Locale.ROOT))) {
            ClassLoader cl = e.getPlugin().getClass().getClassLoader();
            watched.add(new Watched(e.getPlugin().getName(), System.currentTimeMillis(), new WeakReference<>(cl), String.valueOf(cl)));
        }
        // our MONITOR listener must run after the newly enabled plugin's listeners
        pe.reRegisterMonitor();
        Bukkit.getGlobalRegionScheduler().runDelayed(this, t -> pe.reRegisterMonitor(), 40);
    }

    @EventHandler(priority = EventPriority.MONITOR)
    public void onPluginDisable(PluginDisableEvent e) {
        if (e.getPlugin() == this) return;
        JsonObject o = new JsonObject();
        o.addProperty("t", System.currentTimeMillis());
        o.addProperty("event", "disable");
        o.addProperty("plugin", e.getPlugin().getName());
        bounded(pluginEvents, o);
    }

    private static void bounded(List<JsonObject> l, JsonObject o) {
        synchronized (l) {
            if (l.size() < 5000) l.add(o);
        }
    }

    // ------------------------------------------------------------------------------------------------
    // commands
    // ------------------------------------------------------------------------------------------------

    private static final String HELP = String.join("\n",
            "probe reset | dump <file> | mark <text> | status | hash <on|off> | accounting <on|off> | relisten",
            "probe arena | tp <p> <x> <y> <z> [yaw pitch] | gm <p> <mode> | give <p> <material> [n] [slot] | slot <p> <0-8>",
            "probe clearinv <p> | effect <p> <effect> <seconds> <amp> | cleareffects <p> | velocity <p> <vx> <vy> <vz>",
            "probe heal <p> | flight <p> <true|false> | mount <p> <boat|minecart> | dismount <p>",
            "probe setblock <x> <y> <z> <blockdata> | fill <x1> <y1> <z1> <x2> <y2> <z2> <blockdata>",
            "probe trace <p> <seconds> | trace off | pipeline <p>",
            "probe threads <file> | watch <plugin> | gc <file> | disable-timed <plugin> <file> | pinger <ticks|off>");

    @Override
    public boolean onCommand(@NotNull CommandSender sender, @NotNull Command command, @NotNull String label, String @NotNull [] a) {
        String cmd = a.length == 0 ? "help" : a[0].toLowerCase(Locale.ROOT);
        String line = String.join(" ", a);
        try {
            switch (cmd) {
                case "help" -> sender.sendMessage(HELP);
                case "reset" -> {
                    stats.clear();
                    tick.reset();
                    taps.pipelines.clear();
                    pe.traces.clear();
                    kicks.clear();
                    quits.clear();
                    joins.clear();
                    resetAtMs = System.currentTimeMillis();
                    ok(line);
                }
                case "dump" -> {
                    Path p = resolve(a[1]);
                    Files.createDirectories(p.toAbsolutePath().getParent());
                    Files.writeString(p, new GsonBuilder().setPrettyPrinting().serializeSpecialFloatingPointValues().create().toJson(dump()), StandardCharsets.UTF_8);
                    ok(line);
                }
                case "mark" -> {
                    getLogger().info("[probe] MARK " + String.join(" ", Arrays.copyOfRange(a, 1, a.length)));
                    ok(line);
                }
                case "status" -> {
                    JsonObject c = stats.groupsJson();
                    getLogger().info("[probe] STATUS online=" + Bukkit.getOnlinePlayers().size() + " kicks=" + kicks.size()
                            + " conns=" + taps.conns.size() + " groups=" + c.keySet());
                    ok(line);
                }
                case "hash" -> {
                    pe.hashBuffers = a[1].equalsIgnoreCase("on");
                    ok(line);
                }
                case "accounting" -> {
                    enabled = a[1].equalsIgnoreCase("on");
                    ok(line);
                }
                case "relisten" -> {
                    pe.reRegisterMonitor();
                    ok(line);
                }
                case "arena" -> {
                    World w = Bukkit.getWorlds().get(0);
                    arena.build(w); // logs "[probe] ARENA READY" when complete
                    ok(line);
                }
                case "tp" -> {
                    Player p = player(a[1]);
                    // on the player's own thread: leave a vehicle first (Folia refuses to teleport passengers)
                    var task = p.getScheduler().run(this, t -> {
                        Location cur = p.getLocation();
                        Location l = new Location(p.getWorld(), d(a[2]), d(a[3]), d(a[4]),
                                a.length > 5 ? (float) d(a[5]) : cur.getYaw(), a.length > 6 ? (float) d(a[6]) : cur.getPitch());
                        if (p.isInsideVehicle()) p.leaveVehicle();
                        p.teleportAsync(l).whenComplete((r, err) -> {
                            if (err != null || !Boolean.TRUE.equals(r)) err(line, "teleportAsync=" + r + " " + err);
                            else ok(line);
                        });
                    }, () -> err(line, "player retired"));
                    if (task == null) err(line, "player scheduler retired");
                }
                case "gm" -> onPlayer(a[1], line, p -> p.setGameMode(GameMode.valueOf(a[2].toUpperCase(Locale.ROOT))));
                case "give" -> onPlayer(a[1], line, p -> {
                    Material m = Material.matchMaterial(a[2]);
                    if (m == null) throw new IllegalArgumentException("unknown material " + a[2]);
                    ItemStack it = new ItemStack(m, a.length > 3 ? Integer.parseInt(a[3]) : 1);
                    if (a.length > 4) p.getInventory().setItem(Integer.parseInt(a[4]), it);
                    else p.getInventory().addItem(it);
                });
                case "slot" -> onPlayer(a[1], line, p -> p.getInventory().setHeldItemSlot(Integer.parseInt(a[2])));
                case "clearinv" -> onPlayer(a[1], line, p -> p.getInventory().clear());
                case "effect" -> onPlayer(a[1], line, p -> {
                    PotionEffectType t = Registry.EFFECT.get(NamespacedKey.minecraft(a[2].toLowerCase(Locale.ROOT)));
                    if (t == null) throw new IllegalArgumentException("unknown effect " + a[2]);
                    p.addPotionEffect(new PotionEffect(t, Integer.parseInt(a[3]) * 20, Integer.parseInt(a[4]), false, false));
                });
                case "cleareffects" -> onPlayer(a[1], line, p -> {
                    for (PotionEffect pe : p.getActivePotionEffects()) p.removePotionEffect(pe.getType());
                });
                case "velocity" -> onPlayer(a[1], line, p -> p.setVelocity(new Vector(d(a[2]), d(a[3]), d(a[4]))));
                case "heal" -> onPlayer(a[1], line, p -> {
                    var attr = p.getAttribute(org.bukkit.attribute.Attribute.MAX_HEALTH);
                    p.setHealth(attr == null ? 20 : attr.getValue());
                    p.setFoodLevel(20);
                    p.setSaturation(20);
                    p.setFireTicks(0);
                });
                case "flight" -> onPlayer(a[1], line, p -> p.setAllowFlight(Boolean.parseBoolean(a[2])));
                case "mount" -> onPlayer(a[1], line, p -> {
                    EntityType t = a[2].equalsIgnoreCase("boat") ? EntityType.OAK_BOAT : EntityType.MINECART;
                    Entity v = p.getWorld().spawnEntity(p.getLocation(), t);
                    v.addPassenger(p);
                });
                case "dismount" -> onPlayer(a[1], line, p -> {
                    Entity v = p.getVehicle();
                    p.leaveVehicle();
                    if (v != null) v.remove();
                });
                case "setblock" -> {
                    World w = Bukkit.getWorlds().get(0);
                    int x = Integer.parseInt(a[1]), y = Integer.parseInt(a[2]), z = Integer.parseInt(a[3]);
                    String data = a[4];
                    Bukkit.getRegionScheduler().execute(this, w, x >> 4, z >> 4, () -> {
                        try {
                            w.getBlockAt(x, y, z).setBlockData(Bukkit.createBlockData(data), false);
                            ok(line);
                        } catch (Throwable t) {
                            err(line, String.valueOf(t));
                        }
                    });
                }
                case "fill" -> fill(a, line);
                case "trace" -> {
                    if (a[1].equalsIgnoreCase("off")) pe.traces.clear();
                    else pe.traces.put(a[1].toLowerCase(Locale.ROOT), new PeProbe.Trace(Integer.parseInt(a[2]), 100_000));
                    ok(line);
                }
                case "pipeline" -> {
                    Player p = player(a[1]);
                    Object ch = PacketEvents.getAPI().getPlayerManager().getChannel(p);
                    if (ch instanceof io.netty.channel.Channel c) getLogger().info("[probe] PIPELINE " + p.getName() + " " + c.pipeline().names());
                    ok(line);
                }
                case "threads" -> {
                    JsonObject o = threadsJson();
                    Path p = resolve(a[1]);
                    Files.createDirectories(p.toAbsolutePath().getParent());
                    Files.writeString(p, new GsonBuilder().setPrettyPrinting().create().toJson(o), StandardCharsets.UTF_8);
                    ok(line);
                }
                case "pinger" -> {
                    // emulates a second ping-sending plugin (anticheat transactions); NEVER used in shadow-invariant runs
                    if (pingerTask != null) { pingerTask.cancel(); pingerTask = null; }
                    if (!a[1].equalsIgnoreCase("off")) {
                        long period = Long.parseLong(a[1]);
                        pingerTask = Bukkit.getGlobalRegionScheduler().runAtFixedRate(this, t -> {
                            for (Player p : Bukkit.getOnlinePlayers()) {
                                var u = PacketEvents.getAPI().getPlayerManager().getUser(p);
                                if (u != null) u.sendPacket(new com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPing(pingId.incrementAndGet()));
                            }
                        }, period, period);
                    }
                    ok(line);
                }
                case "watch" -> {
                    watchedNames.add(a[1].toLowerCase(Locale.ROOT));
                    Plugin pl = Bukkit.getPluginManager().getPlugin(a[1]);
                    if (pl != null && pl.isEnabled()) {
                        ClassLoader cl = pl.getClass().getClassLoader();
                        watched.add(new Watched(pl.getName(), System.currentTimeMillis(), new WeakReference<>(cl), String.valueOf(cl)));
                    }
                    ok(line);
                }
                case "gc" -> {
                    Path p = resolve(a[1]);
                    Bukkit.getAsyncScheduler().runNow(this, t -> {
                        try {
                            for (int i = 0; i < 8; i++) {
                                System.gc();
                                Thread.sleep(250);
                            }
                            JsonObject o = new JsonObject();
                            JsonArray arr = new JsonArray();
                            synchronized (watched) {
                                for (Watched w : watched) {
                                    JsonObject wo = new JsonObject();
                                    wo.addProperty("plugin", w.plugin());
                                    wo.addProperty("enabled_at_ms", w.enabledAtMs());
                                    wo.addProperty("loader", w.loaderName());
                                    wo.addProperty("collected", w.loader().get() == null);
                                    Plugin cur = Bukkit.getPluginManager().getPlugin(w.plugin());
                                    wo.addProperty("is_current_instance", cur != null && cur.getClass().getClassLoader() == w.loader().get());
                                    arr.add(wo);
                                }
                            }
                            o.add("watched", arr);
                            o.add("threads", threadsJson());
                            o.add("pe_listeners", peListeners());
                            Files.createDirectories(p.toAbsolutePath().getParent());
                            Files.writeString(p, new GsonBuilder().setPrettyPrinting().create().toJson(o), StandardCharsets.UTF_8);
                            ok(line);
                        } catch (Throwable ex) {
                            err(line, String.valueOf(ex));
                        }
                    });
                }
                case "disable-timed" -> {
                    Plugin pl = Bukkit.getPluginManager().getPlugin(a[1]);
                    if (pl == null) throw new IllegalArgumentException("no plugin " + a[1]);
                    boolean wasEnabled = pl.isEnabled();
                    long t0 = System.nanoTime();
                    Bukkit.getPluginManager().disablePlugin(pl);
                    long dt = System.nanoTime() - t0;
                    JsonObject o = new JsonObject();
                    o.addProperty("plugin", pl.getName());
                    o.addProperty("was_enabled", wasEnabled);
                    o.addProperty("disable_ms", dt / 1e6);
                    o.addProperty("thread", Thread.currentThread().getName());
                    o.add("threads_after", threadsJson());
                    Path p = resolve(a[2]);
                    Files.createDirectories(p.toAbsolutePath().getParent());
                    Files.writeString(p, new GsonBuilder().setPrettyPrinting().create().toJson(o), StandardCharsets.UTF_8);
                    getLogger().info("[probe] DISABLE-TIMED " + pl.getName() + " ms=" + (dt / 1e6));
                    ok(line);
                }
                default -> err(line, "unknown command, see probe help");
            }
        } catch (Throwable t) {
            err(line, String.valueOf(t));
        }
        return true;
    }

    private void fill(String[] a, String line) {
        World w = Bukkit.getWorlds().get(0);
        int x1 = Integer.parseInt(a[1]), y1 = Integer.parseInt(a[2]), z1 = Integer.parseInt(a[3]);
        int x2 = Integer.parseInt(a[4]), y2 = Integer.parseInt(a[5]), z2 = Integer.parseInt(a[6]);
        String data = a[7];
        int minX = Math.min(x1, x2), maxX = Math.max(x1, x2), minZ = Math.min(z1, z2), maxZ = Math.max(z1, z2);
        int minY = Math.min(y1, y2), maxY = Math.max(y1, y2);
        java.util.concurrent.atomic.AtomicInteger left = new java.util.concurrent.atomic.AtomicInteger();
        List<int[]> chunks = new ArrayList<>();
        for (int cx = minX >> 4; cx <= maxX >> 4; cx++) for (int cz = minZ >> 4; cz <= maxZ >> 4; cz++) chunks.add(new int[]{cx, cz});
        left.set(chunks.size());
        for (int[] c : chunks) {
            w.getChunkAtAsync(c[0], c[1], true).whenComplete((ch, e) -> Bukkit.getRegionScheduler().execute(this, w, c[0], c[1], () -> {
                try {
                    var bd = Bukkit.createBlockData(data);
                    for (int x = Math.max(minX, c[0] << 4); x <= Math.min(maxX, (c[0] << 4) + 15); x++)
                        for (int z = Math.max(minZ, c[1] << 4); z <= Math.min(maxZ, (c[1] << 4) + 15); z++)
                            for (int y = minY; y <= maxY; y++) w.getBlockAt(x, y, z).setBlockData(bd, false);
                } catch (Throwable t) {
                    warn("fill", t);
                } finally {
                    if (left.decrementAndGet() == 0) ok(line);
                }
            }));
        }
    }

    private interface PlayerTask {
        void run(Player p) throws Exception;
    }

    private void onPlayer(String name, String line, PlayerTask task) {
        Player p = player(name);
        var scheduled = p.getScheduler().run(this, t -> {
            try {
                task.run(p);
                ok(line);
            } catch (Throwable ex) {
                err(line, String.valueOf(ex));
            }
        }, () -> err(line, "player retired"));
        if (scheduled == null) err(line, "player scheduler retired");
    }

    private Player player(String name) {
        Player p = Bukkit.getPlayerExact(name);
        if (p == null) throw new IllegalArgumentException("player not online: " + name);
        return p;
    }

    private static double d(String s) {
        return Double.parseDouble(s);
    }

    private Path resolve(String f) {
        Path p = Path.of(f);
        return p.isAbsolute() ? p : serverDir().resolve(p);
    }

    private void ok(String line) {
        getLogger().info("[probe] OK " + line);
    }

    private void err(String line, String why) {
        getLogger().warning("[probe] ERR " + line + " :: " + why);
    }

    // ------------------------------------------------------------------------------------------------
    // dump
    // ------------------------------------------------------------------------------------------------

    private JsonObject threadsJson() {
        JsonObject o = new JsonObject();
        Set<String> names = new TreeSet<>();
        Set<String> pe = new TreeSet<>();
        for (Thread t : Thread.getAllStackTraces().keySet()) {
            names.add(t.getName());
            if (t.getName().startsWith("PandaEye")) pe.add(t.getName() + (t.isDaemon() ? " (daemon)" : "") + " " + t.getState());
        }
        o.addProperty("count", names.size());
        o.add("pandaeye", Stats.arr(pe));
        o.add("all", Stats.arr(names));
        return o;
    }

    /** Class names of every registered PacketEvents listener (unwrapping PacketListener.asAbstract wrappers). */
    static JsonArray peListeners() {
        JsonArray a = new JsonArray();
        try {
            Object em = PacketEvents.getAPI().getEventManager();
            java.lang.reflect.Field f = em.getClass().getDeclaredField("listeners");
            f.setAccessible(true);
            Object[] ls = (Object[]) f.get(em);
            for (Object l : ls) {
                String n = l.getClass().getName();
                for (java.lang.reflect.Field inner : l.getClass().getDeclaredFields()) {
                    if (inner.getName().startsWith("this$")) {
                        inner.setAccessible(true);
                        Object o = inner.get(l);
                        if (o != null) n = o.getClass().getName() + " (via asAbstract)";
                    }
                }
                String prio = "?";
                try { prio = String.valueOf(((com.github.retrooper.packetevents.event.PacketListenerCommon) l).getPriority()); } catch (Throwable ignored) { }
                a.add(prio + " " + n);
            }
        } catch (Throwable t) {
            a.add("error: " + t);
        }
        return a;
    }

    private JsonObject dump() {
        JsonObject root = new JsonObject();
        JsonObject meta = new JsonObject();
        meta.addProperty("time_ms", System.currentTimeMillis());
        meta.addProperty("since_reset_ms", System.currentTimeMillis() - resetAtMs);
        meta.addProperty("server", Bukkit.getName());
        meta.addProperty("server_version", Bukkit.getVersion());
        meta.addProperty("minecraft_version", Bukkit.getMinecraftVersion());
        meta.addProperty("folia", isFolia());
        meta.addProperty("pe_version", String.valueOf(PacketEvents.getAPI().getVersion()));
        meta.addProperty("pe_reencode_by_default", PacketEvents.getAPI().getSettings().reEncodeByDefault());
        meta.addProperty("hash_buffers", pe.hashBuffers);
        JsonArray plugins = new JsonArray();
        for (Plugin p : Bukkit.getPluginManager().getPlugins()) {
            JsonObject po = new JsonObject();
            po.addProperty("name", p.getName());
            po.addProperty("version", p.getPluginMeta().getVersion());
            po.addProperty("enabled", p.isEnabled());
            plugins.add(po);
        }
        meta.add("plugins", plugins);
        JsonArray online = new JsonArray();
        for (Player p : Bukkit.getOnlinePlayers()) online.add(p.getName());
        meta.add("online", online);
        root.add("meta", meta);
        root.add("counters", stats.groupsJson());
        root.add("costs", stats.histsJson());
        root.add("tick", tick.json());
        root.add("cpu", TickMonitor.cpuJson());
        synchronized (kicks) {
            JsonArray k = new JsonArray();
            kicks.forEach(k::add);
            root.add("kicks", k);
        }
        synchronized (quits) {
            JsonArray q = new JsonArray();
            quits.forEach(q::add);
            root.add("quits", q);
        }
        synchronized (joins) {
            JsonArray j = new JsonArray();
            joins.forEach(j::add);
            root.add("joins", j);
        }
        synchronized (pluginEvents) {
            JsonArray j = new JsonArray();
            pluginEvents.forEach(j::add);
            root.add("plugin_events", j);
        }
        JsonObject pipes = new JsonObject();
        for (Map.Entry<String, List<String>> e : new java.util.TreeMap<>(taps.pipelines).entrySet()) pipes.add(e.getKey(), Stats.arr(e.getValue()));
        root.add("pipelines", pipes);
        JsonObject pend = new JsonObject();
        taps.pendingQueues().forEach(pend::addProperty);
        root.add("inflight_pending", pend);
        root.add("traces", pe.tracesJson());
        root.add("threads", threadsJson());
        root.add("pe_listeners", peListeners());
        return root;
    }

    @SuppressWarnings("unused")
    private static long ms(long ns) {
        return TimeUnit.NANOSECONDS.toMillis(ns);
    }
}
