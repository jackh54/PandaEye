package dev.pandascript.pandaeye.paper.enforce;

import com.github.retrooper.packetevents.event.ProtocolPacketEvent;
import com.github.retrooper.packetevents.netty.buffer.ByteBufHelper;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;
import com.tngtech.archunit.base.DescribedPredicate;
import com.tngtech.archunit.core.domain.JavaClasses;
import com.tngtech.archunit.core.domain.JavaConstructorCall;
import com.tngtech.archunit.core.domain.JavaMethodCall;
import com.tngtech.archunit.core.importer.ClassFileImporter;
import com.tngtech.archunit.core.importer.ImportOption;
import com.tngtech.archunit.lang.ArchRule;
import dev.pandascript.pandaeye.paper.packet.RawPeek;
import dev.pandascript.pandaeye.paper.packet.Wrappers;
import org.bukkit.Bukkit;
import org.bukkit.Server;
import org.bukkit.entity.Entity;
import org.bukkit.entity.Player;
import org.bukkit.event.Cancellable;
import org.bukkit.event.Listener;
import org.bukkit.event.entity.EntityDamageEvent;
import org.bukkit.event.player.PlayerVelocityEvent;
import org.bukkit.plugin.Plugin;
import org.bukkit.plugin.PluginManager;
import org.bukkit.util.Vector;
import org.junit.jupiter.api.Test;

import java.util.Set;
import java.util.stream.Collectors;

import static com.tngtech.archunit.lang.syntax.ArchRuleDefinition.noClasses;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Shadow-mode invariant (ARCHITECTURE 1.1, 4.2): only {@link LiveEnforcer} may call APIs that affect players
 * and only it creates (and registers) the {@link MitigationListener}, which itself changes events only through
 * the live enforcer; Bukkit listeners are registered only by the live enforcer and the runtime (whose
 * listeners are read-only MONITOR ones);
 * only {@link Wrappers} may touch an event's last-used wrapper; only {@link Wrappers} and {@link RawPeek} may
 * move a buffer's reader index (and they restore it); nobody calls wrapper setters or buffer writers.
 */
class EnforcerArchitectureTest {

    private static final JavaClasses PLUGIN = new ClassFileImporter()
            .withImportOption(ImportOption.Predefined.DO_NOT_INCLUDE_TESTS)
            .importPackages("dev.pandascript.pandaeye");

    static final DescribedPredicate<JavaMethodCall> SIDE_EFFECT = new DescribedPredicate<>("a player-affecting API") {
        @Override
        public boolean test(JavaMethodCall call) {
            String name = call.getName();
            var owner = call.getTargetOwner();
            if (owner.isAssignableTo(User.class)) {
                return name.startsWith("sendPacket") || name.startsWith("writePacket")
                        || name.startsWith("receivePacket") || Set.of("flushPackets", "closeConnection",
                        "closeInventory", "sendMessage", "sendTitle").contains(name);
            }
            if (owner.isAssignableTo(ProtocolPacketEvent.class)) {
                return Set.of("setCancelled", "markForReEncode", "setByteBuf", "setPlayer", "setClientVersion",
                        "setServerVersion").contains(name);
            }
            if (owner.isAssignableTo(PacketWrapper.class)) {
                return name.startsWith("set") || name.startsWith("write") || name.equals("prepareForSend");
            }
            if (owner.isAssignableTo(Cancellable.class) && name.equals("setCancelled")) {
                return true;
            }
            if (owner.isAssignableTo(Entity.class)
                    && Set.of("teleport", "teleportAsync", "setVelocity").contains(name)) {
                return true;
            }
            if (owner.isAssignableTo(Player.class) && (name.equals("kick") || name.equals("kickPlayer"))) {
                return true;
            }
            if ((owner.isEquivalentTo(Bukkit.class) || owner.isAssignableTo(Server.class))
                    && name.equals("dispatchCommand")) {
                return true;
            }
            if (owner.isAssignableTo(PlayerVelocityEvent.class) && name.equals("setVelocity")) {
                return true;
            }
            return owner.isAssignableTo(EntityDamageEvent.class) && name.startsWith("setDamage");
        }
    };

    static final DescribedPredicate<JavaMethodCall> LISTENER_REGISTRATION = new DescribedPredicate<>("Bukkit listener registration") {
        @Override
        public boolean test(JavaMethodCall call) {
            return call.getTargetOwner().isAssignableTo(PluginManager.class)
                    && (call.getName().equals("registerEvents") || call.getName().equals("registerEvent"));
        }
    };

    static final DescribedPredicate<JavaConstructorCall> NEW_MITIGATION_LISTENER = new DescribedPredicate<>("new MitigationListener") {
        @Override
        public boolean test(JavaConstructorCall call) {
            return call.getTargetOwner().isEquivalentTo(MitigationListener.class);
        }
    };

    private static final String RUNTIME = "dev.pandascript.pandaeye.paper.runtime.Runtime";
    private static final String VERDICT_PLATFORM = "dev.pandascript.pandaeye.paper.verdict.PaperVerdictPlatform";

    static final DescribedPredicate<JavaMethodCall> CORRECTIVE_UNBAN = new DescribedPredicate<>("Enforcement.correctiveUnban") {
        @Override
        public boolean test(JavaMethodCall call) {
            return call.getTargetOwner().isEquivalentTo(Enforcement.class) && call.getName().equals("correctiveUnban");
        }
    };

    static final DescribedPredicate<JavaMethodCall> LAST_USED_WRAPPER = new DescribedPredicate<>("setLastUsedWrapper") {
        @Override
        public boolean test(JavaMethodCall call) {
            return call.getTargetOwner().isAssignableTo(ProtocolPacketEvent.class)
                    && call.getName().equals("setLastUsedWrapper");
        }
    };

    static final DescribedPredicate<JavaMethodCall> BUFFER_MUTATION = new DescribedPredicate<>("a buffer mutation") {
        @Override
        public boolean test(JavaMethodCall call) {
            if (!call.getTargetOwner().isEquivalentTo(ByteBufHelper.class)) {
                return false;
            }
            String name = call.getName();
            return name.startsWith("write") || name.startsWith("set") || name.equals("clear") || name.equals("release")
                    || name.equals("retain") || name.equals("capacity") && call.getTarget().getRawParameterTypes().size() == 2
                    || name.equals("writerIndex") && call.getTarget().getRawParameterTypes().size() == 2
                    || name.startsWith("reset") || name.startsWith("mark");
        }
    };

    static final DescribedPredicate<JavaMethodCall> READER_INDEX = new DescribedPredicate<>("reader index moves") {
        @Override
        public boolean test(JavaMethodCall call) {
            if (!call.getTargetOwner().isEquivalentTo(ByteBufHelper.class)) {
                return false;
            }
            String name = call.getName();
            return name.startsWith("read") && !(name.equals("readerIndex") && call.getTarget().getRawParameterTypes().size() == 1)
                    || name.equals("skipBytes");
        }
    };

    @Test
    void onlyTheLiveEnforcerAffectsPlayers() {
        ArchRule rule = noClasses().that().doNotHaveFullyQualifiedName(LiveEnforcer.class.getName())
                .should().callMethodWhere(SIDE_EFFECT);
        rule.check(PLUGIN);
    }

    @Test
    void onlyTheLiveEnforcerCreatesTheMitigationListener() {
        noClasses().that().doNotHaveFullyQualifiedName(LiveEnforcer.class.getName())
                .should().callConstructorWhere(NEW_MITIGATION_LISTENER).check(PLUGIN);
    }

    @Test
    void bukkitListenersAreRegisteredOnlyByTheLiveEnforcerAndTheRuntime() {
        noClasses().that().doNotHaveFullyQualifiedName(LiveEnforcer.class.getName())
                .and().doNotHaveFullyQualifiedName(RUNTIME)
                .should().callMethodWhere(LISTENER_REGISTRATION).check(PLUGIN);
    }

    @Test
    void theMitigationListenerChangesEventsOnlyThroughTheLiveEnforcer() {
        JavaClasses classes = new ClassFileImporter().importClasses(MitigationListener.class, LiveEnforcer.class);
        Set<JavaMethodCall> calls = classes.get(MitigationListener.class).getMethodCallsFromSelf();
        assertTrue(calls.stream().noneMatch(SIDE_EFFECT::test));
        Set<String> viaLive = calls.stream().filter(call -> call.getTargetOwner().isEquivalentTo(LiveEnforcer.class))
                .map(JavaMethodCall::getName).collect(Collectors.toSet());
        assertEquals(Set.of("cancel", "setDamage", "setVelocity"), viaLive);
        assertTrue(classes.get(LiveEnforcer.class).getConstructorCallsFromSelf().stream().anyMatch(NEW_MITIGATION_LISTENER::test));
        assertTrue(classes.get(LiveEnforcer.class).getMethodCallsFromSelf().stream().anyMatch(LISTENER_REGISTRATION::test));
    }

    @Test
    void onlyTheVerdictPlatformUsesTheUnbanException() {
        noClasses().that().doNotHaveFullyQualifiedName(VERDICT_PLATFORM)
                .should().callMethodWhere(CORRECTIVE_UNBAN).check(PLUGIN);
    }

    @Test
    void theListenerRulesCatchViolations() {
        JavaClasses violator = new ClassFileImporter().importClasses(Violator.class);
        assertThrows(AssertionError.class,
                () -> noClasses().should().callMethodWhere(LISTENER_REGISTRATION).check(violator));
    }

    @Test
    void onlyWrappersRestoresTheLastUsedWrapper() {
        noClasses().that().doNotHaveFullyQualifiedName(Wrappers.class.getName())
                .should().callMethodWhere(LAST_USED_WRAPPER).check(PLUGIN);
    }

    @Test
    void nobodyWritesPacketBuffers() {
        noClasses().should().callMethodWhere(BUFFER_MUTATION).check(PLUGIN);
    }

    @Test
    void onlyTheRestoringHelpersMoveTheReaderIndex() {
        noClasses().that().doNotHaveFullyQualifiedName(Wrappers.class.getName())
                .and().doNotHaveFullyQualifiedName(RawPeek.class.getName())
                .should().callMethodWhere(READER_INDEX).check(PLUGIN);
    }

    @Test
    void thePredicateMatchesTheRealCallsOfTheLiveEnforcer() {
        JavaClasses live = new ClassFileImporter().importClasses(LiveEnforcer.class);
        assertTrue(live.get(LiveEnforcer.class).getMethodCallsFromSelf().stream().filter(SIDE_EFFECT).count() >= 6);
    }

    @Test
    void theRuleCatchesAViolation() {
        JavaClasses violator = new ClassFileImporter().importClasses(Violator.class);
        ArchRule rule = noClasses().should().callMethodWhere(SIDE_EFFECT);
        assertThrows(AssertionError.class, () -> rule.check(violator));
        assertFalse(violator.get(Violator.class).getMethodCallsFromSelf().stream().noneMatch(SIDE_EFFECT));
    }

    /** Never instantiated: exists so the rule is shown to fire. */
    @SuppressWarnings("unused")
    static final class Violator {
        void push(Player player) {
            player.setVelocity(new Vector(0, 1, 0));
        }

        void register(Plugin plugin, Listener listener) {
            Bukkit.getPluginManager().registerEvents(listener, plugin);
        }
    }
}
