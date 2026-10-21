package dev.pandascript.pandaeye.velocity;

import com.tngtech.archunit.base.DescribedPredicate;
import com.tngtech.archunit.core.domain.JavaClasses;
import com.tngtech.archunit.core.domain.JavaMethodCall;
import com.tngtech.archunit.core.importer.ClassFileImporter;
import com.tngtech.archunit.core.importer.ImportOption;
import com.velocitypowered.api.command.CommandManager;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ServerConnection;
import org.junit.jupiter.api.Test;

import java.util.Set;

import static com.tngtech.archunit.lang.syntax.ArchRuleDefinition.noClasses;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The proxy's shadow invariant: only {@link LiveProxyEnforcer} disconnects players or runs commands; nothing is
 * ever sent to a client; only the tracker sends (the session message) to backends.
 */
class ProxyEnforcerArchitectureTest {

    private static final JavaClasses PLUGIN = new ClassFileImporter()
            .withImportOption(ImportOption.Predefined.DO_NOT_INCLUDE_TESTS)
            .importPackages("dev.pandascript.pandaeye.velocity");

    static final DescribedPredicate<JavaMethodCall> PLAYER_EFFECT = new DescribedPredicate<>("a player-affecting API") {
        @Override
        public boolean test(JavaMethodCall call) {
            String name = call.getName();
            if (call.getTargetOwner().isAssignableTo(CommandManager.class)) {
                return name.startsWith("execute");
            }
            return call.getTargetOwner().isAssignableTo(Player.class) && Set.of("disconnect", "spoofChatInput",
                    "transferToHost", "createConnectionRequest").contains(name);
        }
    };

    static final DescribedPredicate<JavaMethodCall> TO_CLIENT = new DescribedPredicate<>("a plugin message to a client") {
        @Override
        public boolean test(JavaMethodCall call) {
            return call.getTargetOwner().isAssignableTo(Player.class) && call.getName().equals("sendPluginMessage");
        }
    };

    static final DescribedPredicate<JavaMethodCall> TO_BACKEND = new DescribedPredicate<>("a plugin message to a backend") {
        @Override
        public boolean test(JavaMethodCall call) {
            return call.getTargetOwner().isAssignableTo(ServerConnection.class) && call.getName().equals("sendPluginMessage");
        }
    };

    @Test
    void onlyTheLiveProxyEnforcerAffectsPlayers() {
        noClasses().that().doNotHaveFullyQualifiedName(LiveProxyEnforcer.class.getName())
                .should().callMethodWhere(PLAYER_EFFECT).check(PLUGIN);
        assertTrue(new ClassFileImporter().importClasses(LiveProxyEnforcer.class).get(LiveProxyEnforcer.class)
                .getMethodCallsFromSelf().stream().filter(PLAYER_EFFECT::test).count() >= 2);
    }

    @Test
    void onlyTheVerdictPlatformUsesTheUnbanException() {
        noClasses().that().doNotHaveFullyQualifiedName(VelocityVerdictPlatform.class.getName())
                .should().callMethodWhere(new DescribedPredicate<JavaMethodCall>("ProxyEnforcement.correctiveUnban") {
                    @Override
                    public boolean test(JavaMethodCall call) {
                        return call.getTargetOwner().isEquivalentTo(ProxyEnforcement.class)
                                && call.getName().equals("correctiveUnban");
                    }
                }).check(PLUGIN);
    }

    @Test
    void nothingIsSentToClientsAndOnlyTheTrackerTalksToBackends() {
        noClasses().should().callMethodWhere(TO_CLIENT).check(PLUGIN);
        noClasses().that().doNotHaveFullyQualifiedName(ProxyTracker.class.getName())
                .should().callMethodWhere(TO_BACKEND).check(PLUGIN);
    }
}
