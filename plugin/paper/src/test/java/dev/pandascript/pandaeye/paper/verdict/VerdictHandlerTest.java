package dev.pandascript.pandaeye.paper.verdict;

import com.github.retrooper.packetevents.protocol.player.User;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.common.verdict.ExecutedStore;
import dev.pandascript.pandaeye.common.verdict.VerdictNotes;
import dev.pandascript.pandaeye.paper.TestProxies;
import dev.pandascript.pandaeye.paper.config.ConfigLoader;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.enforce.Enforcement;
import dev.pandascript.pandaeye.paper.enforce.EnforcerCounters;
import dev.pandascript.pandaeye.paper.enforce.LiveEnforcer;
import dev.pandascript.pandaeye.paper.enforce.ShadowEnforcer;
import dev.pandascript.pandaeye.paper.log.AlertService;
import dev.pandascript.pandaeye.paper.packet.TestUsers;
import dev.pandascript.pandaeye.paper.platform.Schedulers;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionSignals;
import dev.pandascript.pandaeye.paper.state.ClientInfo;
import dev.pandascript.pandaeye.protocol.v1.Action;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import io.papermc.paper.threadedregions.scheduler.EntityScheduler;
import org.bukkit.configuration.file.YamlConfiguration;
import org.bukkit.entity.Player;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.logging.Logger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The Paper side of verdict handling: in shadow nothing reaches the live enforcer except the UNBAN of an own
 * ban; in live mode with enforcement every action goes through the live enforcer (commands on the global
 * region thread, kicks on the player's scheduler, mitigations on the session).
 */
class VerdictHandlerTest {

    private static final UUID STEVE = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");
    private static final RemoteSettings ENFORCING = new RemoteSettings(4, true, true, true, false, true, 1);

    @TempDir
    Path dir;

    private volatile PluginConfig config;
    private volatile RemoteSettings remote = RemoteSettings.UNKNOWN;
    private final List<Runnable> globalTasks = new ArrayList<>();
    private final List<String> entityTasks = new ArrayList<>();
    private final List<String> acks = new ArrayList<>();
    private final LiveEnforcer live = new LiveEnforcer(null, globalTasks::add);
    private final ShadowEnforcer shadow = new ShadowEnforcer();
    private final Log log = new Log() {
        @Override
        public void info(String message) {
        }

        @Override
        public void warn(String message) {
        }

        @Override
        public void warn(String message, Throwable error) {
            throw new AssertionError(message, error);
        }

        @Override
        public void debug(String message) {
        }
    };
    private Boolean bypass = false;
    private Session session;
    private Player player;
    private VerdictHandler handler;
    private int ids;

    @BeforeEach
    void setUp() throws Exception {
        config = config("shadow");
        EntityScheduler scheduler = TestProxies.of(EntityScheduler.class, (name, args) -> {
            entityTasks.add(name);
            return null;
        });
        player = TestProxies.of(Player.class, Map.of("getUniqueId", STEVE, "getName", "Steve", "getScheduler", scheduler));
        User user = TestUsers.user(STEVE);
        session = new Session(user, STEVE, "Steve", false, new ClientInfo(769, "1.21.4", true, true, true),
                new SessionSignals(false, () -> config, (from, to) -> false), System.nanoTime());
    }

    private static PluginConfig config(String mode) throws Exception {
        YamlConfiguration yaml = new YamlConfiguration();
        yaml.loadFromString("mode: " + mode + "\n");
        return new ConfigLoader().load(yaml);
    }

    private VerdictHandler handler() {
        Enforcement enforcement = new Enforcement(() -> !config.shadow(), () -> remote, shadow, live);
        MitigationController mitigations = new MitigationController(enforcement, () -> config,
                id -> STEVE.equals(id) ? session : null, () -> List.of(session), task -> { }, () -> true, log,
                new RateLimitedLog(log, 1));
        PlayerLookup lookup = new PlayerLookup() {
            @Override
            public Online online(UUID id) {
                return id.equals(STEVE) ? new Online(player, "Steve", bypass, session) : null;
            }

            @Override
            public String cachedName(UUID id) {
                return null;
            }
        };
        AlertService alerts = new AlertService(Logger.getLogger("pandaeye-test"), new Schedulers(null), () -> config);
        return new VerdictHandler(() -> config, enforcement, lookup, mitigations, alerts, null, log, dir,
                (id, executed, note) -> acks.add(executed + "|" + note));
    }

    private Verdict verdict(Action action) {
        return Verdict.newBuilder()
                .setVerdictId("00000000-0000-4000-8000-" + String.format("%012d", ++ids))
                .setPlayer(Uuids.toByteString(STEVE))
                .setAction(action)
                .addMitigations(Mitigation.MITIGATION_HIT_CANCEL)
                .addMitigations(Mitigation.MITIGATION_SETBACK)
                .setStrength(0.5)
                .setReason("Combat heuristics (killaura.hitbox x7)")
                .setCreatedUnixMs(System.currentTimeMillis())
                .build();
    }

    private void sendEveryEnforcingAction() {
        for (Action action : List.of(Action.ACTION_ALERT, Action.ACTION_MITIGATE, Action.ACTION_UNMITIGATE,
                Action.ACTION_KICK, Action.ACTION_BAN)) {
            handler.handle(verdict(action));
        }
    }

    @Test
    void shadowModeExecutesNothingEvenWithCloudEnforcementOn() {
        remote = ENFORCING;
        handler = handler();
        sendEveryEnforcingAction();
        assertEquals(0, live.counters().total(), "the live enforcer is never used in shadow mode");
        assertTrue(globalTasks.isEmpty());
        assertTrue(entityTasks.isEmpty());
        assertFalse(session.mitigation().active());
        assertEquals(5, acks.size());
        assertTrue(acks.stream().allMatch(ack -> ack.equals("false|" + VerdictNotes.SHADOW_LOCAL)), acks.toString());
    }

    @Test
    void liveModeWithoutKnownCloudEnforcementExecutesNothing() throws Exception {
        config = config("live");
        handler = handler();
        sendEveryEnforcingAction();
        assertEquals(0, live.counters().total());
        assertTrue(acks.stream().allMatch(ack -> ack.equals("false|" + VerdictNotes.SHADOW_ENFORCEMENT_OFF)), acks.toString());
    }

    @Test
    void theUnbanOfAnOwnBanRunsInShadowModeThroughTheLiveEnforcer() throws Exception {
        ExecutedStore store = ExecutedStore.load(dir.resolve(ExecutedStore.FILE_NAME), log, System.currentTimeMillis());
        store.recordExecuted(new ExecutedStore.Executed("ban-1", "BAN", STEVE, System.currentTimeMillis()),
                new ExecutedStore.Ban("ban-1", STEVE, "Steve", System.currentTimeMillis()), System.currentTimeMillis());
        handler = handler();
        handler.handle(verdict(Action.ACTION_UNBAN).toBuilder().setSupersedes("ban-1").setShadow(true).build());
        assertEquals(List.of("true|" + VerdictNotes.UNBANNED), acks);
        assertEquals(1, live.counters().get(EnforcerCounters.Action.COMMAND));
        assertEquals(1, live.counters().total());
        assertEquals(1, globalTasks.size(), "the unban command runs on the global region thread");
        handler.handle(verdict(Action.ACTION_UNBAN).toBuilder().setSupersedes("someone-elses-ban").build());
        assertEquals("false|" + VerdictNotes.NOT_EXECUTED_HERE, acks.get(1));
        assertEquals(1, globalTasks.size());
    }

    @Test
    void liveModeWithEnforcementActsThroughTheLiveEnforcer() throws Exception {
        config = config("live");
        remote = ENFORCING;
        handler = handler();
        handler.handle(verdict(Action.ACTION_MITIGATE));
        assertEquals(Set.of(Mitigation.MITIGATION_HIT_CANCEL, Mitigation.MITIGATION_SETBACK), session.mitigations());
        assertEquals(0.5, session.mitigation().strength());
        handler.handle(verdict(Action.ACTION_UNMITIGATE));
        assertFalse(session.mitigation().active());
        handler.handle(verdict(Action.ACTION_KICK));
        assertEquals(List.of("run"), entityTasks, "player.kick runs on the player's scheduler");
        handler.handle(verdict(Action.ACTION_BAN));
        assertEquals(1, globalTasks.size(), "the ban command runs on the global region thread");
        assertEquals(List.of("true|" + VerdictNotes.MITIGATED, "true|" + VerdictNotes.UNMITIGATED,
                "true|" + VerdictNotes.KICKED, "true|" + VerdictNotes.BANNED), acks);
        assertEquals(1, live.counters().get(EnforcerCounters.Action.KICK));
        assertEquals(1, live.counters().get(EnforcerCounters.Action.COMMAND));
        assertEquals(0, shadow.counters().total());
    }

    @Test
    void enforcementSwitchedOffClearsMitigationsAndStopsNewOnes() throws Exception {
        config = config("live");
        remote = ENFORCING;
        handler = handler();
        handler.handle(verdict(Action.ACTION_MITIGATE));
        assertTrue(session.mitigation().active());
        assertTrue(handler.mitigations().allowed());
        assertTrue(handler.mitigations().state(player).active(), "the listener sees the mitigation");
        remote = new RemoteSettings(4, true, true, true, false, false, 2);
        assertEquals(null, handler.mitigations().state(player), "the listener re-checks the gate on every event");
        handler.remoteSettingsChanged();
        assertFalse(session.mitigation().active(), "active mitigations are cleared when enforcement turns off");
        handler.handle(verdict(Action.ACTION_MITIGATE));
        assertEquals("false|" + VerdictNotes.SHADOW_ENFORCEMENT_OFF, acks.get(1));
        assertFalse(session.mitigation().active());
    }

    @Test
    void playersWithBypassOrNotYetEvaluatedBypassAreExempt() throws Exception {
        config = config("live");
        remote = ENFORCING;
        bypass = null;
        handler = handler();
        handler.handle(verdict(Action.ACTION_KICK));
        bypass = true;
        handler.handle(verdict(Action.ACTION_BAN));
        assertEquals(List.of("false|" + VerdictNotes.EXEMPT, "false|" + VerdictNotes.EXEMPT), acks);
        assertEquals(0, live.counters().total());
    }

    @Test
    void executedVerdictsAreNotRepeatedAfterARestart() throws Exception {
        config = config("live");
        remote = ENFORCING;
        handler = handler();
        Verdict ban = verdict(Action.ACTION_BAN);
        handler.handle(ban);
        handler = handler();
        handler.handle(ban);
        assertEquals(List.of("true|" + VerdictNotes.BANNED, "true|" + VerdictNotes.ALREADY_EXECUTED), acks);
        assertEquals(1, globalTasks.size());
    }
}
