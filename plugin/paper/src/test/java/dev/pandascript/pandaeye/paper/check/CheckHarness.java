package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.paper.state.Move;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.SessionEvents;
import dev.pandascript.pandaeye.protocol.v1.GameMode;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Function;

/**
 * Drives one simulated player through the real {@link PacketProcessor}, {@link PacketState} and checks with
 * a synthetic clock, records flags and candidates, and runs confirmations against a {@link FakeWorld}. No
 * Bukkit or PacketEvents is involved. See {@code plugin/paper/CHECKS-FRAMEWORK.md}.
 */
public final class CheckHarness {

    public static final long TICK_NANOS = 50_000_000L;
    public static final ClientInfo TICK_CLIENT = new ClientInfo(774, "1.21.11", true, true, true);
    public static final ClientInfo LEGACY_CLIENT = new ClientInfo(47, "1.8.9", false, false, false);

    /** A confirmed violation as the flag pipeline saw it. */
    public record Flag(String check, Violation violation, double vl, boolean worldConfirmed, List<Move> context) {
    }

    /** A candidate waiting for {@link #confirmAll()}. */
    public record Pending(CheckHandle handle, Candidate candidate) {
    }

    private final FakeSignals signals = new FakeSignals();
    private final FakeWorld world = new FakeWorld();
    private final List<Flag> flags = new ArrayList<>();
    private final List<Pending> candidates = new ArrayList<>();
    private final List<String> discards = new ArrayList<>();
    private final Map<String, CheckSettings> settings = new HashMap<>();
    private final PacketState state;
    private final PacketProcessor processor;
    private long now = 1_000_000_000_000L;
    private double x;
    private double y = 64;
    private double z;

    private CheckHarness(ClientInfo client, List<Function<CheckDescriptor, Check>> factories, List<String> ids) {
        state = new PacketState(client, signals);
        List<CheckHandle> handles = new ArrayList<>();
        FlagSink flagSink = (handle, violation, vl, context, confirmed, nanos) ->
                flags.add(new Flag(handle.descriptor().id(), violation, vl, confirmed, context));
        CandidateSink candidateSink = (handle, candidate) -> candidates.add(new Pending(handle, candidate));
        for (int i = 0; i < factories.size(); i++) {
            String id = ids.get(i);
            Check check = factories.get(i).apply(CheckCatalog.get(id));
            handles.add(new CheckHandle(check, () -> settings.getOrDefault(id, CheckSettings.DEFAULT), state, flagSink,
                    candidateSink));
        }
        PlayerChecks checks = new PlayerChecks(state, handles, (site, error) -> {
            throw new AssertionError(site, error);
        });
        processor = new PacketProcessor(state, checks, SessionEvents.NONE, nanos -> nanos / 1_000);
    }

    /** A harness for one check, created through its catalog descriptor. */
    public static CheckHarness of(String id, Function<CheckDescriptor, Check> factory) {
        return new CheckHarness(TICK_CLIENT, List.of(factory), List.of(id));
    }

    public static CheckHarness of(ClientInfo client, String id, Function<CheckDescriptor, Check> factory) {
        return new CheckHarness(client, List.of(factory), List.of(id));
    }

    public CheckHarness settings(String id, CheckSettings value) {
        settings.put(id, value);
        return this;
    }

    // ---------------------------------------------------------------------------------------------
    // Packets
    // ---------------------------------------------------------------------------------------------

    /** JOIN_GAME in survival, abilities (not flying), a keep-alive exchange, and one tick end. */
    public CheckHarness join() {
        processor.joinGame(1, GameMode.GAME_MODE_SURVIVAL, "minecraft:overworld", null, now);
        processor.serverAbilities(new Abilities(false, false, false, 0.05f), now);
        processor.tickingState(20, false, now);
        acknowledge();
        processor.playerLoaded();
        processor.tickEnd(now);
        return this;
    }

    /** Server keep-alive now and the client's reply one tick later: everything sent so far is acknowledged. */
    public CheckHarness acknowledge() {
        long id = now;
        processor.serverKeepAlive(id, now);
        now += 1_000_000;
        processor.clientKeepAlive(id, now);
        return this;
    }

    /** One client tick: a position packet at the given feet position, then CLIENT_TICK_END. */
    public CheckHarness move(double nx, double ny, double nz, boolean onGround) {
        processor.flying(nx, ny, nz, 0, 0, true, false, onGround, false, false, now);
        x = nx;
        y = ny;
        z = nz;
        tickEnd();
        return this;
    }

    /** One client tick moving {@code dx, dy, dz} from the last position. */
    public CheckHarness step(double dx, double dy, double dz, boolean onGround) {
        return move(x + dx, y + dy, z + dz, onGround);
    }

    /** One client tick with a rotation-only packet. */
    public CheckHarness look(float yaw, float pitch) {
        processor.flying(0, 0, 0, yaw, pitch, false, true, true, false, false, now);
        return tickEnd();
    }

    /** CLIENT_TICK_END and 50 ms. */
    public CheckHarness tickEnd() {
        processor.tickEnd(now);
        now += TICK_NANOS;
        return this;
    }

    /** {@code count} standing ticks on the ground (warms up the 40-packet exemption). */
    public CheckHarness stand(int count) {
        for (int i = 0; i < count; i++) {
            move(x, y, z, true);
        }
        return this;
    }

    public CheckHarness advance(long nanos) {
        now += nanos;
        return this;
    }

    // ---------------------------------------------------------------------------------------------
    // Confirmation and results
    // ---------------------------------------------------------------------------------------------

    /** Runs every pending candidate's confirmer against the fake world, as the entity thread would. */
    public CheckHarness confirmAll() {
        for (Pending pending : candidates) {
            Candidate candidate = pending.candidate();
            ConfirmContext context = new ConfirmContext() {
                @Override
                public WorldView world() {
                    return world;
                }

                @Override
                public List<Position> positions() {
                    return candidate.positions();
                }

                @Override
                public double scale() {
                    return 1;
                }
            };
            switch (candidate.confirmer().confirm(context)) {
                case Confirmation.Confirmed confirmed -> pending.handle().confirmedByWorld(candidate,
                        confirmed.violation() == null ? candidate.violation() : confirmed.violation(), now);
                case Confirmation.Discarded discarded -> {
                    pending.handle().discardedByWorld();
                    discards.add(discarded.reason());
                }
            }
        }
        candidates.clear();
        return this;
    }

    public PacketProcessor processor() {
        return processor;
    }

    public PacketState state() {
        return state;
    }

    public FakeSignals signals() {
        return signals;
    }

    public FakeWorld world() {
        return world;
    }

    public long now() {
        return now;
    }

    public List<Flag> flags() {
        return flags;
    }

    public List<Pending> candidates() {
        return candidates;
    }

    public List<String> discards() {
        return discards;
    }
}
