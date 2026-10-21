package dev.pandascript.pandaeye.paper.session;

import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.paper.check.CandidateSink;
import dev.pandascript.pandaeye.paper.check.Check;
import dev.pandascript.pandaeye.paper.check.CheckHandle;
import dev.pandascript.pandaeye.paper.check.CheckRegistry;
import dev.pandascript.pandaeye.paper.check.FlagSink;
import dev.pandascript.pandaeye.paper.check.PlayerChecks;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;
import dev.pandascript.pandaeye.paper.state.SessionEvents;

import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.function.BiConsumer;
import java.util.function.Function;
import java.util.function.Predicate;
import java.util.function.Supplier;

/** Builds a fully wired {@link Session}: signals, packet state, one instance of every check, telemetry hooks. */
public final class SessionFactory {

    private final EventClock clock;
    private final Supplier<PluginConfig> config;
    private final SessionSignals.StallClock stalls;
    private final Predicate<UUID> bedrock;
    private final CheckRegistry checks;
    private final Function<Session, FlagSink> flags;
    private final Function<Session, CandidateSink> candidates;
    private final Function<Session, SessionEvents> events;
    private final BiConsumer<String, Throwable> errors;

    public SessionFactory(EventClock clock, Supplier<PluginConfig> config, SessionSignals.StallClock stalls,
                          Predicate<UUID> bedrock, CheckRegistry checks, Function<Session, FlagSink> flags,
                          Function<Session, CandidateSink> candidates, Function<Session, SessionEvents> events,
                          BiConsumer<String, Throwable> errors) {
        this.clock = clock;
        this.config = config;
        this.stalls = stalls;
        this.bedrock = bedrock;
        this.checks = checks;
        this.flags = flags;
        this.candidates = candidates;
        this.events = events;
        this.errors = errors;
    }

    public Session create(User user, UUID uuid, String name, ClientInfo client, boolean hotLoaded, long nowNanos) {
        SessionSignals signals = new SessionSignals(bedrock.test(uuid), config, stalls);
        Session session = new Session(user, uuid, name, hotLoaded, client, signals, nowNanos);
        FlagSink flagSink = flags.apply(session);
        CandidateSink candidateSink = candidates.apply(session);
        List<CheckHandle> handles = new ArrayList<>();
        for (Check check : checks.create()) {
            String id = check.descriptor().id();
            handles.add(new CheckHandle(check, () -> config.get().check(id), session.state(), flagSink, candidateSink));
        }
        PlayerChecks playerChecks = new PlayerChecks(session.state(), handles, errors);
        session.attach(new PacketProcessor(session.state(), playerChecks, events.apply(session), clock::toMicros));
        return session;
    }
}
