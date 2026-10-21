package dev.pandascript.pandaeye.e2e.probe;

import com.google.gson.GsonBuilder;
import com.google.gson.JsonArray;
import com.google.gson.JsonObject;
import org.bukkit.Bukkit;
import org.bukkit.Difficulty;
import org.bukkit.GameRule;
import org.bukkit.Location;
import org.bukkit.Material;
import org.bukkit.World;
import org.bukkit.block.Block;
import org.bukkit.block.BlockState;
import org.bukkit.block.Chest;
import org.bukkit.block.data.BlockData;
import org.bukkit.entity.EntityType;
import org.bukkit.inventory.ItemStack;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Builds the test fixtures (stairs, slabs, ledge, tower, pool, chest, stone rows, step blocks, rails, scaffold
 * pillars) on the owning region of every chunk, so it works on Paper and Folia, and writes {@code arena.json}
 * (all fixture coordinates) into the server directory. The bots read arena.json; it is the single source of truth.
 *
 * World: flat, layers bedrock/dirt/dirt/grass, biome the_void, so the standing level is y = -60 (Y0).
 */
final class Arena {
    static final int Y0 = -60;

    private record Op(int x, int y, int z, String data) {
    }

    private record Spawn(double x, double y, double z, EntityType type) {
    }

    private final ProbePlugin probe;
    private final List<Op> ops = new ArrayList<>();
    private final List<Spawn> spawns = new ArrayList<>();
    private final JsonObject fixtures = new JsonObject();

    Arena(ProbePlugin probe) {
        this.probe = probe;
        layout();
    }

    private void set(int x, int y, int z, String data) {
        ops.add(new Op(x, y, z, data));
    }

    private void fill(int x1, int y1, int z1, int x2, int y2, int z2, String data) {
        for (int x = Math.min(x1, x2); x <= Math.max(x1, x2); x++)
            for (int y = Math.min(y1, y2); y <= Math.max(y1, y2); y++)
                for (int z = Math.min(z1, z2); z <= Math.max(z1, z2); z++) set(x, y, z, data);
    }

    private static JsonArray v(double... xs) {
        JsonArray a = new JsonArray();
        for (double x : xs) a.add(x);
        return a;
    }

    private void fixture(String name, JsonObject o) {
        fixtures.add(name, o);
    }

    private void layout() {
        JsonObject o;
        // spawn / generic
        o = new JsonObject();
        o.add("pos", v(0.5, Y0, 0.5));
        fixture("spawn", o);

        // stairs: oak stairs ascending +x, then a platform
        for (int i = 0; i < 5; i++) {
            if (i > 0) fill(10 + i, Y0, 20, 10 + i, Y0 + i - 1, 22, "minecraft:stone");
            fill(10 + i, Y0 + i, 20, 10 + i, Y0 + i, 22, "minecraft:oak_stairs[facing=east]");
        }
        fill(15, Y0, 20, 17, Y0 + 4, 22, "minecraft:stone");
        o = new JsonObject();
        o.add("start", v(8.5, Y0, 21.5));
        o.add("end", v(16.5, Y0 + 5, 21.5));
        o.addProperty("dir", "+x");
        fixture("stairs", o);

        // slabs: surface rises 0.5 per block along +x
        for (int i = 0; i < 10; i++) {
            double h = (i + 1) * 0.5;
            int full = (int) Math.floor(h);
            if (full > 0) fill(10 + i, Y0, 26, 10 + i, Y0 + full - 1, 28, "minecraft:stone");
            if (h - full > 0.25) fill(10 + i, Y0 + full, 26, 10 + i, Y0 + full, 28, "minecraft:stone_slab[type=bottom]");
        }
        fill(20, Y0, 26, 22, Y0 + 4, 28, "minecraft:stone");
        o = new JsonObject();
        o.add("start", v(8.5, Y0, 27.5));
        o.add("end", v(21.5, Y0 + 5, 27.5));
        o.addProperty("dir", "+x");
        fixture("slabs", o);

        // ledge: 3-high platform, walk off -x
        fill(-16, Y0, -16, -12, Y0 + 2, -12, "minecraft:stone");
        o = new JsonObject();
        o.add("top", v(-13.5, Y0 + 3, -13.5));
        o.add("below", v(-19.5, Y0, -13.5));
        o.addProperty("height", 3);
        fixture("ledge", o);

        // tower: 15-high, for falls
        fill(-26, Y0, -16, -24, Y0 + 14, -14, "minecraft:stone");
        o = new JsonObject();
        o.add("top", v(-24.5, Y0 + 15, -14.5));
        o.add("fall_column", v(-29.5, Y0 + 15, -14.5));
        o.addProperty("height", 15);
        fixture("tower", o);

        // pool: 8x8, 3 deep, surface at Y0
        fill(-24, Y0 - 3, 10, -17, Y0 - 1, 17, "minecraft:water");
        o = new JsonObject();
        o.add("min", v(-24, Y0 - 3, 10));
        o.add("max", v(-17, Y0 - 1, 17));
        o.add("entry", v(-15.5, Y0, 13.5));
        o.add("boat", v(-20.5, Y0, 13.5));
        fixture("pool", o);
        spawns.add(new Spawn(-20.5, Y0 - 0.4, 13.5, EntityType.OAK_BOAT));

        // chest with items
        set(4, Y0, -6, "minecraft:chest[facing=south]");
        o = new JsonObject();
        o.add("block", v(4, Y0, -6));
        o.add("stand", v(4.5, Y0, -4.5));
        fixture("chest", o);

        // stone rows for digging (2 high), diggers stand at z = -16.5 facing north
        fill(30, Y0, -20, 45, Y0 + 1, -18, "minecraft:stone");
        o = new JsonObject();
        o.add("min", v(30, Y0, -20));
        o.add("max", v(45, Y0 + 1, -18));
        o.add("stand", v(31.5, Y0, -16.5));
        o.addProperty("face_toward", "-z");
        fixture("stone", o);

        // step blocks: heights 1,2,3 along +x
        fill(50, Y0, 0, 50, Y0, 4, "minecraft:stone");
        fill(51, Y0, 0, 51, Y0 + 1, 4, "minecraft:stone");
        fill(52, Y0, 0, 52, Y0 + 2, 4, "minecraft:stone");
        o = new JsonObject();
        o.add("start", v(48.5, Y0, 2.5));
        o.addProperty("first_x", 50);
        o.addProperty("dir", "+x");
        fixture("steps", o);

        // rails with a minecart
        fill(-10, Y0, -30, 10, Y0, -30, "minecraft:rail[shape=east_west]");
        o = new JsonObject();
        o.add("start", v(-9.5, Y0, -29.5));
        o.add("end", v(9.5, Y0, -29.5));
        fixture("rails", o);
        spawns.add(new Spawn(-9.5, Y0, -29.5, EntityType.MINECART));

        // scaffold pillars: a row of single blocks to place against
        for (int i = 0; i < 6; i++) set(60 + i * 3, Y0, 0, "minecraft:stone");
        o = new JsonObject();
        o.add("first", v(60, Y0, 0));
        o.addProperty("spacing", 3);
        o.addProperty("count", 6);
        fixture("pillars", o);

        // open areas (no blocks, documented only)
        o = new JsonObject();
        o.add("min", v(20, Y0, -10));
        o.add("max", v(30, Y0, -2));
        fixture("build", o);
        o = new JsonObject();
        o.add("center", v(6.5, Y0, 46.5));
        fixture("combat", o);
        o = new JsonObject();
        o.add("origin", v(-63.5, Y0, 100.5));
        o.addProperty("lane_spacing", 4);
        o.addProperty("lanes", 48);
        o.addProperty("length", 128);
        o.addProperty("dir", "+x");
        fixture("lanes", o);
    }

    /** Builds asynchronously; logs "[probe] ARENA READY" and writes arena.json when every chunk is done. */
    void build(World world) {
        Bukkit.getGlobalRegionScheduler().execute(probe, () -> {
            try {
                world.setDifficulty(Difficulty.PEACEFUL);
                world.setSpawnLocation(0, Y0, 0);
                world.setStorm(false);
                world.setThundering(false);
                world.setClearWeatherDuration(Integer.MAX_VALUE / 2);
                world.setTime(6000);
            } catch (Throwable t) {
                probe.warn("arena-world", t);
            }
            gamerule(world, GameRule.DO_DAYLIGHT_CYCLE, false);
            gamerule(world, GameRule.DO_WEATHER_CYCLE, false);
            gamerule(world, GameRule.DO_MOB_SPAWNING, false);
            gamerule(world, GameRule.DO_PATROL_SPAWNING, false);
            gamerule(world, GameRule.DO_TRADER_SPAWNING, false);
            gamerule(world, GameRule.DO_INSOMNIA, false);
            gamerule(world, GameRule.KEEP_INVENTORY, true);
            gamerule(world, GameRule.DO_IMMEDIATE_RESPAWN, true);
            gamerule(world, GameRule.ANNOUNCE_ADVANCEMENTS, false);
            gamerule(world, GameRule.SPAWN_RADIUS, 0);
            gamerule(world, GameRule.RANDOM_TICK_SPEED, 0);
            gamerule(world, GameRule.DISABLE_RAIDS, true);
        });
        Map<Long, List<Op>> byChunk = new HashMap<>();
        for (Op op : ops) byChunk.computeIfAbsent(key(op.x >> 4, op.z >> 4), k -> new ArrayList<>()).add(op);
        Map<Long, List<Spawn>> spawnsByChunk = new HashMap<>();
        for (Spawn s : spawns) {
            long k = key(((int) Math.floor(s.x)) >> 4, ((int) Math.floor(s.z)) >> 4);
            spawnsByChunk.computeIfAbsent(k, x -> new ArrayList<>()).add(s);
            byChunk.computeIfAbsent(k, x -> new ArrayList<>());
        }
        AtomicInteger remaining = new AtomicInteger(byChunk.size());
        for (Map.Entry<Long, List<Op>> e : byChunk.entrySet()) {
            int cx = (int) (e.getKey() >> 32);
            int cz = (int) (long) e.getKey();
            List<Spawn> sp = spawnsByChunk.getOrDefault(e.getKey(), List.of());
            world.getChunkAtAsync(cx, cz, true).whenComplete((chunk, err) ->
                    Bukkit.getRegionScheduler().execute(probe, world, cx, cz, () -> {
                        try {
                            for (Op op : e.getValue()) {
                                Block b = world.getBlockAt(op.x, op.y, op.z);
                                BlockData d = Bukkit.createBlockData(op.data);
                                if (!b.getBlockData().equals(d)) b.setBlockData(d, false);
                                if (d.getMaterial() == Material.CHEST) fillChest(b);
                            }
                            for (Spawn s : sp) {
                                Location l = new Location(world, s.x, s.y, s.z);
                                boolean exists = !world.getNearbyEntities(l, 1.5, 1.5, 1.5, en -> en.getType() == s.type).isEmpty();
                                if (!exists) world.spawnEntity(l, s.type);
                            }
                        } catch (Throwable t) {
                            probe.warn("arena-chunk", t);
                        } finally {
                            if (remaining.decrementAndGet() == 0) done(world);
                        }
                    }));
        }
    }

    private static <T> void gamerule(World w, GameRule<T> rule, T value) {
        try {
            w.setGameRule(rule, value);
        } catch (Throwable ignored) {
        }
    }

    private void fillChest(Block b) {
        BlockState st = b.getState();
        if (st instanceof Chest c) {
            c.getBlockInventory().clear();
            c.getBlockInventory().setItem(0, new ItemStack(Material.COBBLESTONE, 32));
            c.getBlockInventory().setItem(1, new ItemStack(Material.OAK_PLANKS, 16));
            c.getBlockInventory().setItem(2, new ItemStack(Material.STICK, 8));
            c.getBlockInventory().setItem(3, new ItemStack(Material.BREAD, 5));
        }
    }

    private void done(World world) {
        JsonObject root = new JsonObject();
        root.addProperty("world", world.getName());
        root.addProperty("y0", Y0);
        root.add("fixtures", fixtures);
        try {
            Path p = probe.serverDir().resolve("arena.json");
            Files.writeString(p, new GsonBuilder().setPrettyPrinting().create().toJson(root), StandardCharsets.UTF_8);
        } catch (IOException ex) {
            probe.warn("arena-json", ex);
        }
        probe.getLogger().info("[probe] ARENA READY ops=" + ops.size() + " spawns=" + spawns.size());
    }

    private static long key(int cx, int cz) {
        return ((long) cx << 32) | (cz & 0xFFFFFFFFL);
    }
}
