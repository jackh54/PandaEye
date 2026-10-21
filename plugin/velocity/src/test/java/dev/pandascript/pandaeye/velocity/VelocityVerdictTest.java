package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.command.CommandManager;
import com.velocitypowered.api.proxy.ConsoleCommandSource;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ProxyServer;
import dev.pandascript.pandaeye.common.transport.RemoteSettings;
import dev.pandascript.pandaeye.common.util.Uuids;
import dev.pandascript.pandaeye.common.verdict.ExecutedStore;
import dev.pandascript.pandaeye.common.verdict.VerdictNotes;
import dev.pandascript.pandaeye.common.verdict.VerdictProcessor;
import dev.pandascript.pandaeye.protocol.v1.Action;
import dev.pandascript.pandaeye.protocol.v1.Mitigation;
import dev.pandascript.pandaeye.protocol.v1.Verdict;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Verdicts on the proxy: shadow by default, proxy commands in live mode with enforcement, the UNBAN exception. */
class VelocityVerdictTest {

    private static final UUID STEVE = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");
    private static final RemoteSettings ENFORCING = new RemoteSettings(4, true, true, true, false, true, 1);

    @TempDir
    Path dir;

    private final List<String> commands = new ArrayList<>();
    private final List<String> disconnects = new ArrayList<>();
    private final List<String> acks = new ArrayList<>();
    private volatile RemoteSettings remote = RemoteSettings.UNKNOWN;
    private int ids;

    private final Player steve = TestProxies.of(Player.class, (name, args) -> switch (name) {
        case "getUniqueId" -> STEVE;
        case "getUsername" -> "Steve";
        case "disconnect" -> {
            disconnects.add(String.valueOf(args[0]));
            yield null;
        }
        default -> null;
    });
    private final CommandManager commandManager = TestProxies.of(CommandManager.class, (name, args) -> {
        if (name.equals("executeAsync")) {
            commands.add((String) args[1]);
            return CompletableFuture.completedFuture(true);
        }
        return null;
    });
    private final ProxyServer server = TestProxies.of(ProxyServer.class, (name, args) -> switch (name) {
        case "getPlayer" -> STEVE.equals(args[0]) ? Optional.of(steve) : Optional.empty();
        case "getCommandManager" -> commandManager;
        case "getConsoleCommandSource" -> TestProxies.of(ConsoleCommandSource.class, Map.of());
        default -> null;
    });

    private VerdictProcessor processor(String yaml) {
        VelocityConfig config = SessionMessageTest.config(yaml);
        ProxyEnforcement enforcement = new ProxyEnforcement(config::live, () -> remote, new ShadowProxyEnforcer(),
                new LiveProxyEnforcer(server, SessionMessageTest.QUIET));
        ExecutedStore store = ExecutedStore.load(dir.resolve(ExecutedStore.FILE_NAME), SessionMessageTest.QUIET,
                System.currentTimeMillis());
        return new VerdictProcessor(new VelocityVerdictPlatform(server, () -> config, enforcement, new NameCache(),
                new ConsoleAlerts(SessionMessageTest.QUIET)), store,
                (id, executed, note) -> acks.add(executed + "|" + note), SessionMessageTest.QUIET);
    }

    private Verdict verdict(Action action) {
        return Verdict.newBuilder().setVerdictId("v-" + ++ids).setPlayer(Uuids.toByteString(STEVE)).setAction(action)
                .setReason("Fly (movement.fly x3)").setCreatedUnixMs(System.currentTimeMillis())
                .addMitigations(Mitigation.MITIGATION_HIT_CANCEL).setAppealCode("AB12").build();
    }

    private static final String LIVE = """
            mode: live
            punishments:
              ban-command: "ban %player% %reason% (appeal %appeal_code%)"
              unban-command: "unban %player%"
            """;

    @Test
    void shadowIsTheDefaultAndExecutesNothing() {
        remote = ENFORCING;
        VerdictProcessor processor = processor("");
        for (Action action : List.of(Action.ACTION_ALERT, Action.ACTION_MITIGATE, Action.ACTION_KICK, Action.ACTION_BAN)) {
            processor.handle(verdict(action));
        }
        assertTrue(commands.isEmpty());
        assertTrue(disconnects.isEmpty());
        assertTrue(acks.stream().allMatch(a -> a.equals("false|" + VerdictNotes.SHADOW_LOCAL)), acks.toString());
    }

    @Test
    void liveModeNeedsTheCloudSwitch() {
        VerdictProcessor processor = processor(LIVE);
        processor.handle(verdict(Action.ACTION_KICK));
        assertEquals(List.of("false|" + VerdictNotes.SHADOW_ENFORCEMENT_OFF), acks);
        assertTrue(disconnects.isEmpty());
    }

    @Test
    void liveModeWithEnforcementRunsProxyCommands() {
        remote = ENFORCING;
        VerdictProcessor processor = processor(LIVE);
        processor.handle(verdict(Action.ACTION_KICK));
        assertEquals(1, disconnects.size(), "no kick-command: disconnect");
        Verdict ban = verdict(Action.ACTION_BAN);
        processor.handle(ban);
        assertEquals(List.of("ban Steve Fly (movement.fly x3) (appeal AB12)"), commands);
        processor.handle(verdict(Action.ACTION_MITIGATE));
        assertEquals("false|" + VerdictNotes.MITIGATIONS_UNSUPPORTED, acks.get(2));
        remote = RemoteSettings.UNKNOWN;
        processor.handle(verdict(Action.ACTION_UNBAN).toBuilder().setSupersedes(ban.getVerdictId()).build());
        assertEquals(List.of("ban Steve Fly (movement.fly x3) (appeal AB12)", "unban Steve"), commands,
                "the unban of an own ban runs even with enforcement off");
        assertEquals("true|" + VerdictNotes.UNBANNED, acks.get(3));
    }

    @Test
    void theCircuitBreakerAppliesOnTheProxyToo() {
        remote = ENFORCING;
        VerdictProcessor processor = processor(LIVE);
        for (int i = 0; i < 4; i++) {
            processor.handle(verdict(Action.ACTION_BAN));
        }
        assertEquals(3, commands.size());
        assertEquals("false|" + VerdictNotes.CIRCUIT_OPEN, acks.get(3));
    }
}
