package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.event.ProtocolPacketEvent;
import com.github.retrooper.packetevents.protocol.attribute.Attributes;
import com.github.retrooper.packetevents.protocol.entity.data.EntityData;
import com.github.retrooper.packetevents.protocol.entity.data.EntityDataTypes;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.packettype.PacketTypeCommon;
import com.github.retrooper.packetevents.protocol.particle.Particle;
import com.github.retrooper.packetevents.protocol.particle.type.ParticleTypes;
import com.github.retrooper.packetevents.protocol.player.DiggingAction;
import com.github.retrooper.packetevents.protocol.player.GameMode;
import com.github.retrooper.packetevents.protocol.player.InteractionHand;
import com.github.retrooper.packetevents.protocol.player.User;
import com.github.retrooper.packetevents.protocol.potion.PotionTypes;
import com.github.retrooper.packetevents.protocol.sound.Sounds;
import com.github.retrooper.packetevents.protocol.teleport.RelativeFlag;
import com.github.retrooper.packetevents.protocol.util.WeightedList;
import com.github.retrooper.packetevents.protocol.world.BlockFace;
import com.github.retrooper.packetevents.protocol.world.Difficulty;
import com.github.retrooper.packetevents.protocol.world.Location;
import com.github.retrooper.packetevents.protocol.world.dimension.DimensionTypeRef;

import com.github.retrooper.packetevents.resources.ResourceLocation;
import com.github.retrooper.packetevents.util.Vector3d;
import com.github.retrooper.packetevents.util.Vector3f;
import com.github.retrooper.packetevents.util.Vector3i;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;
import com.github.retrooper.packetevents.wrapper.configuration.client.WrapperConfigClientPluginMessage;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientAnimation;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientAttack;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientClickWindow;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientClientTickEnd;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientCloseWindow;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientEntityAction;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientHeldItemChange;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientInteractEntity;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientKeepAlive;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerAbilities;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerBlockPlacement;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerDigging;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerFlying;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerInput;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPlayerLoaded;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPluginMessage;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPong;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientTeleportConfirm;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientUseItem;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientVehicleMove;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerChangeGameState;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerCloseWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerDeathCombatEvent;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityEffect;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityMetadata;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityVelocity;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerExplosion;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerFacePlayer;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerHeldItemChange;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerJoinGame;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerKeepAlive;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerOpenHorseWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerOpenWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPing;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerAbilities;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerPositionAndLook;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerRotation;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerRemoveEntityEffect;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerRespawn;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerSetPassengers;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerTickingState;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerUpdateAttributes;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerUpdateHealth;
import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.time.EventClock;
import dev.pandascript.pandaeye.common.transport.EventSink;
import dev.pandascript.pandaeye.common.util.RateLimitedLog;
import dev.pandascript.pandaeye.paper.check.CheckRegistry;
import dev.pandascript.pandaeye.paper.config.ConfigLoader;
import dev.pandascript.pandaeye.paper.config.PluginConfig;
import dev.pandascript.pandaeye.paper.session.EntityIndex;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.session.SessionFactory;
import dev.pandascript.pandaeye.paper.session.SessionRegistry;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.telemetry.Telemetry;
import io.netty.buffer.ByteBuf;
import io.netty.buffer.ByteBufUtil;
import net.kyori.adventure.text.Component;
import org.bukkit.configuration.file.YamlConfiguration;
import org.bukkit.entity.Player;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Supplier;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * ARCHITECTURE 4.2 "Wrapper hygiene": for every packet type the listener handles, real PacketEvents events
 * (server 1.21.11, reEncodeByDefault on) leave the listener with the same last-used wrapper, the same reader
 * index and the same bytes, uncancelled; and the packet was actually read.
 */
class WrapperHygieneTest {

    private static final int SELF = 42;
    private static final int OTHER = 77;

    private record Case(String name, boolean clientBound, PacketTypeCommon type, Supplier<PacketWrapper<?>> wrapper) {
    }

    private final UUID uuid = UUID.randomUUID();
    private final List<String> warnings = new ArrayList<>();
    private User user;
    private PandaEyePacketListener listener;
    private SessionRegistry sessions;

    @BeforeAll
    static void packetEvents() {
        TestPacketEvents.install();
    }

    @BeforeEach
    void setUp() {
        PluginConfig config = new ConfigLoader().load(new YamlConfiguration());
        EventClock clock = EventClock.system();
        EntityIndex entities = new EntityIndex();
        Telemetry telemetry = new Telemetry(new EventSink(100_000), clock, () -> config, entities);
        SessionFactory factory = new SessionFactory(clock, () -> config, (from, to) -> false, id -> false,
                CheckRegistry.defaults(), session -> (handle, violation, vl, context, confirmed, nanos) -> {
                }, session -> (handle, candidate) -> false, telemetry::forSession, (site, error) -> {
                    throw new AssertionError(site, error);
                });
        sessions = new SessionRegistry(entities, factory, new SessionRegistry.Lifecycle() {
            @Override
            public void attached(Session session, Player player) {
            }

            @Override
            public void closed(Session session, String reason) {
            }
        });
        ActiveGate gate = new ActiveGate();
        gate.open();
        listener = new PandaEyePacketListener(gate, sessions, () -> config, (session, nanos) -> {
        }, new PacketCost(), new RateLimitedLog(recordingLog(), 1));
        user = TestPacketEvents.user(uuid);
    }

    @Test
    void everyHandledPacketLeavesTheEventUntouched() {
        List<PacketWrapper<?>> priors = new ArrayList<>();
        priors.add(null);
        priors.add(new WrapperPlayServerPing(99));
        for (PacketWrapper<?> prior : priors) {
            setUp();
            for (Case c : cases()) {
                // ATTACK only exists from 26.1 on; it is still decoded by type, so it is exercised too.
                assertTrue(c.type() == PacketType.Play.Client.ATTACK || c.type().getId(TestPacketEvents.CLIENT) >= 0,
                        c.name() + " has no id in 1.21.11");
                ByteBuf buffer = TestPacketEvents.encode(c.wrapper().get());
                ProtocolPacketEvent event = c.clientBound()
                        ? TestPacketEvents.send(user, c.type(), buffer)
                        : TestPacketEvents.receive(user, c.type(), buffer);
                event.setLastUsedWrapper(prior);
                byte[] before = ByteBufUtil.getBytes(buffer, 0, buffer.writerIndex());
                int readerIndex = buffer.readerIndex();
                boolean reEncode = event.needsReEncode();
                if (event instanceof PacketSendEvent send) {
                    listener.onPacketSend(send);
                } else {
                    listener.onPacketReceive((PacketReceiveEvent) event);
                }
                assertSame(prior, event.getLastUsedWrapper(), c.name() + ": last used wrapper");
                assertEquals(readerIndex, buffer.readerIndex(), c.name() + ": reader index");
                assertEquals(before.length, buffer.writerIndex(), c.name() + ": writer index");
                assertArrayEquals(before, ByteBufUtil.getBytes(buffer, 0, buffer.writerIndex()), c.name() + ": bytes");
                assertFalse(event.isCancelled(), c.name() + ": cancelled");
                assertEquals(reEncode, event.needsReEncode(), c.name() + ": re-encode flag");
                assertTrue(warnings.isEmpty(), c.name() + ": " + warnings);
            }
        }
    }

    @Test
    void packetsAreActuallyRead() {
        run(cases());
        Session session = sessions.get(user);
        assertNotNull(session);
        PacketState state = session.state();
        assertEquals(SELF, state.entityId());
        assertTrue(state.velocities().pending(), "own velocity tracked");
        assertEquals(0.2, state.attributes().lenientMax(AttributeTracker.MOVEMENT_SPEED), 1e-9,
                "sprinting modifier excluded, other modifiers applied");
        assertTrue(state.effects().present("speed"));
        assertEquals("vanilla", state.brand());
        assertEquals(3, state.slots().mirror());
        assertTrue(state.tick() > 0, "tick ends counted");
        assertTrue(state.positionKnown());
        assertEquals(12.5, state.x(), 1e-9);
        assertTrue(state.dead(), "health 0");
        assertTrue(state.containers().known());
        assertTrue(state.inVehicle().latest(), "passenger of the vehicle");
        assertFalse(state.tickRateNormal(), "ticking state 10 tps");
        assertTrue(warnings.isEmpty(), warnings.toString());
    }

    @Test
    void aWrapperAnotherPluginAttachedIsReadInsteadOfTheBuffer() {
        run(List.of(joinGame()));
        WrapperPlayClientPlayerFlying theirs = new WrapperPlayClientPlayerFlying(true, true, true, false,
                new Location(1, 2, 3, 4, 5));
        ByteBuf buffer = TestPacketEvents.encode(new WrapperPlayClientPlayerFlying(true, true, true, false,
                new Location(7, 8, 9, 0, 0)));
        PacketReceiveEvent event = TestPacketEvents.receive(user, PacketType.Play.Client.PLAYER_POSITION_AND_ROTATION,
                buffer);
        event.setLastUsedWrapper(theirs);
        listener.onPacketReceive(event);
        assertSame(theirs, event.getLastUsedWrapper());
        assertEquals(0, buffer.readerIndex());
        assertEquals(1, sessions.get(user).state().x(), 1e-9);
    }

    @Test
    void entityScopedPacketsForOtherEntitiesAreSkippedWithoutDecoding() {
        run(List.of(joinGame()));
        ByteBuf buffer = TestPacketEvents.encode(new WrapperPlayServerEntityVelocity(OTHER, new Vector3d(1, 1, 1)));
        PacketSendEvent event = TestPacketEvents.send(user, PacketType.Play.Server.ENTITY_VELOCITY, buffer);
        listener.onPacketSend(event);
        assertFalse(sessions.get(user).state().velocities().pending());
        assertEquals(0, buffer.readerIndex());
    }

    @Test
    void cancelledClientBoundPacketsAreIgnoredButCancelledServerBoundOnesAreProcessed() {
        run(List.of(joinGame()));
        PacketSendEvent velocity = TestPacketEvents.send(user, PacketType.Play.Server.ENTITY_VELOCITY,
                TestPacketEvents.encode(new WrapperPlayServerEntityVelocity(SELF, new Vector3d(0, 1, 0))));
        velocity.setCancelled(true);
        listener.onPacketSend(velocity);
        assertFalse(sessions.get(user).state().velocities().pending());

        PacketReceiveEvent move = TestPacketEvents.receive(user, PacketType.Play.Client.PLAYER_POSITION,
                TestPacketEvents.encode(new WrapperPlayClientPlayerFlying(true, false, true, false,
                        new Location(5, 64, 5, 0, 0))));
        move.setCancelled(true);
        listener.onPacketReceive(move);
        PacketState state = sessions.get(user).state();
        assertEquals(5, state.x(), 1e-9);
        assertTrue(state.moves().get(0).cancelled());
    }

    private void run(List<Case> cases) {
        for (Case c : cases) {
            ByteBuf buffer = TestPacketEvents.encode(c.wrapper().get());
            if (c.clientBound()) {
                listener.onPacketSend(TestPacketEvents.send(user, c.type(), buffer));
            } else {
                listener.onPacketReceive(TestPacketEvents.receive(user, c.type(), buffer));
            }
        }
    }

    private static Case joinGame() {
        return server("JOIN_GAME", PacketType.Play.Server.JOIN_GAME, () -> new WrapperPlayServerJoinGame(SELF, false,
                GameMode.SURVIVAL, null, List.of("minecraft:overworld"), null, new DimensionTypeRef.IdRef(0),
                Difficulty.NORMAL, "minecraft:overworld", 0L, 100, 10, 10, false, true, false, false, false, null, 0, 63,
                true));
    }

    private static List<Case> cases() {
        Location at = new Location(12.5, 64, -3, 90, 10);
        return List.of(
                joinGame(),
                config("CONFIG_PLUGIN_MESSAGE", PacketType.Configuration.Client.PLUGIN_MESSAGE,
                        () -> new WrapperConfigClientPluginMessage(new ResourceLocation("minecraft", "brand"),
                                brand("ignored-config-brand"))),
                server("RESPAWN", PacketType.Play.Server.RESPAWN, () -> new WrapperPlayServerRespawn(
                        new DimensionTypeRef.IdRef(0), "minecraft:overworld", Difficulty.NORMAL, 0L, GameMode.SURVIVAL,
                        GameMode.SURVIVAL, false, false, (byte) 0, null, 0, 63)),
                server("PLAYER_ABILITIES", PacketType.Play.Server.PLAYER_ABILITIES,
                        () -> new WrapperPlayServerPlayerAbilities(false, false, false, false, 0.05f, 0.1f)),
                server("PLAYER_POSITION_AND_LOOK", PacketType.Play.Server.PLAYER_POSITION_AND_LOOK,
                        () -> new WrapperPlayServerPlayerPositionAndLook(7, new Vector3d(12.5, 64, -3),
                                new Vector3d(0, 0, 0), 90, 10, new RelativeFlag(0))),
                client("TELEPORT_CONFIRM", PacketType.Play.Client.TELEPORT_CONFIRM,
                        () -> new WrapperPlayClientTeleportConfirm(7)),
                client("PLAYER_POSITION_AND_ROTATION", PacketType.Play.Client.PLAYER_POSITION_AND_ROTATION,
                        () -> new WrapperPlayClientPlayerFlying(true, true, false, false, at)),
                client("CLIENT_TICK_END", PacketType.Play.Client.CLIENT_TICK_END, WrapperPlayClientClientTickEnd::new),
                client("PLAYER_POSITION", PacketType.Play.Client.PLAYER_POSITION,
                        () -> new WrapperPlayClientPlayerFlying(true, false, true, true, at)),
                client("PLAYER_ROTATION", PacketType.Play.Client.PLAYER_ROTATION,
                        () -> new WrapperPlayClientPlayerFlying(false, true, true, false, at)),
                client("PLAYER_FLYING", PacketType.Play.Client.PLAYER_FLYING,
                        () -> new WrapperPlayClientPlayerFlying(false, false, true, false, at)),
                server("PLAYER_ROTATION (server)", PacketType.Play.Server.PLAYER_ROTATION,
                        () -> new WrapperPlayServerPlayerRotation(10, false, 5, false)),
                server("FACE_PLAYER", PacketType.Play.Server.FACE_PLAYER, () -> new WrapperPlayServerFacePlayer(
                        WrapperPlayServerFacePlayer.EntitySection.EYES, new Vector3d(0, 70, 0), null)),
                server("ENTITY_VELOCITY", PacketType.Play.Server.ENTITY_VELOCITY,
                        () -> new WrapperPlayServerEntityVelocity(SELF, new Vector3d(0.4, 0.36, 0))),
                server("ENTITY_VELOCITY (other)", PacketType.Play.Server.ENTITY_VELOCITY,
                        () -> new WrapperPlayServerEntityVelocity(OTHER, new Vector3d(0.4, 0.36, 0))),
                server("EXPLOSION", PacketType.Play.Server.EXPLOSION, () -> new WrapperPlayServerExplosion(
                        new Vector3d(0, 64, 0), 4f, 0, new Vector3d(0.1, 0.2, 0.3),
                        new Particle<>(ParticleTypes.EXPLOSION), Sounds.ENTITY_GENERIC_EXPLODE, new WeightedList<>())),
                server("CHANGE_GAME_STATE", PacketType.Play.Server.CHANGE_GAME_STATE,
                        () -> new WrapperPlayServerChangeGameState(WrapperPlayServerChangeGameState.Reason.CHANGE_GAME_MODE, 0)),
                server("ENTITY_EFFECT", PacketType.Play.Server.ENTITY_EFFECT,
                        () -> new WrapperPlayServerEntityEffect(SELF, PotionTypes.SPEED, 1, 200, (byte) 0)),
                server("ENTITY_EFFECT (other)", PacketType.Play.Server.ENTITY_EFFECT,
                        () -> new WrapperPlayServerEntityEffect(OTHER, PotionTypes.SLOWNESS, 1, 200, (byte) 0)),
                server("REMOVE_ENTITY_EFFECT", PacketType.Play.Server.REMOVE_ENTITY_EFFECT,
                        () -> new WrapperPlayServerRemoveEntityEffect(SELF, PotionTypes.JUMP_BOOST)),
                server("UPDATE_ATTRIBUTES", PacketType.Play.Server.UPDATE_ATTRIBUTES,
                        () -> new WrapperPlayServerUpdateAttributes(SELF, List.of(new WrapperPlayServerUpdateAttributes.Property(
                                Attributes.MOVEMENT_SPEED, 0.1, List.of(
                                new WrapperPlayServerUpdateAttributes.PropertyModifier(ResourceLocation.minecraft("sprinting"),
                                        0.3, WrapperPlayServerUpdateAttributes.PropertyModifier.Operation.MULTIPLY_TOTAL),
                                new WrapperPlayServerUpdateAttributes.PropertyModifier(ResourceLocation.minecraft("effect.speed"),
                                        1.0, WrapperPlayServerUpdateAttributes.PropertyModifier.Operation.MULTIPLY_TOTAL)))))),
                server("UPDATE_ATTRIBUTES (other)", PacketType.Play.Server.UPDATE_ATTRIBUTES,
                        () -> new WrapperPlayServerUpdateAttributes(OTHER, List.of(new WrapperPlayServerUpdateAttributes.Property(
                                Attributes.MOVEMENT_SPEED, 0.7, List.of())))),
                server("ENTITY_METADATA", PacketType.Play.Server.ENTITY_METADATA,
                        () -> new WrapperPlayServerEntityMetadata(SELF, List.of(
                                new EntityData<>(0, EntityDataTypes.BYTE, (byte) 0x08),
                                new EntityData<>(8, EntityDataTypes.BYTE, (byte) 0x01)))),
                server("ENTITY_METADATA (other)", PacketType.Play.Server.ENTITY_METADATA,
                        () -> new WrapperPlayServerEntityMetadata(OTHER, List.of(new EntityData<>(0, EntityDataTypes.BYTE, (byte) 0x80)))),
                server("TICKING_STATE", PacketType.Play.Server.TICKING_STATE, () -> new WrapperPlayServerTickingState(10, false)),
                server("OPEN_WINDOW", PacketType.Play.Server.OPEN_WINDOW,
                        () -> new WrapperPlayServerOpenWindow(3, 2, Component.text("Chest"))),
                server("OPEN_HORSE_WINDOW", PacketType.Play.Server.OPEN_HORSE_WINDOW,
                        () -> new WrapperPlayServerOpenHorseWindow(4, 15, OTHER)),
                server("CLOSE_WINDOW", PacketType.Play.Server.CLOSE_WINDOW, () -> new WrapperPlayServerCloseWindow(4)),
                server("KEEP_ALIVE", PacketType.Play.Server.KEEP_ALIVE, () -> new WrapperPlayServerKeepAlive(123L)),
                client("KEEP_ALIVE (client)", PacketType.Play.Client.KEEP_ALIVE, () -> new WrapperPlayClientKeepAlive(123L)),
                server("PING", PacketType.Play.Server.PING, () -> new WrapperPlayServerPing(5)),
                client("PONG", PacketType.Play.Client.PONG, () -> new WrapperPlayClientPong(5)),
                server("HELD_ITEM_CHANGE", PacketType.Play.Server.HELD_ITEM_CHANGE, () -> new WrapperPlayServerHeldItemChange(3)),
                client("HELD_ITEM_CHANGE (client)", PacketType.Play.Client.HELD_ITEM_CHANGE,
                        () -> new WrapperPlayClientHeldItemChange(3)),
                client("INTERACT_ENTITY", PacketType.Play.Client.INTERACT_ENTITY, () -> new WrapperPlayClientInteractEntity(
                        OTHER, WrapperPlayClientInteractEntity.InteractAction.ATTACK, new Vector3d(0, 0, 0),
                        InteractionHand.MAIN_HAND, false)),
                client("ATTACK", PacketType.Play.Client.ATTACK, () -> new WrapperPlayClientAttack(OTHER)),
                client("ANIMATION", PacketType.Play.Client.ANIMATION, () -> new WrapperPlayClientAnimation(InteractionHand.MAIN_HAND)),
                client("PLAYER_BLOCK_PLACEMENT", PacketType.Play.Client.PLAYER_BLOCK_PLACEMENT,
                        () -> new WrapperPlayClientPlayerBlockPlacement(InteractionHand.MAIN_HAND, new Vector3i(1, 63, 1),
                                BlockFace.UP, new Vector3f(0.5f, 1f, 0.5f), null, false, false, 4)),
                client("PLAYER_DIGGING", PacketType.Play.Client.PLAYER_DIGGING, () -> new WrapperPlayClientPlayerDigging(
                        DiggingAction.START_DIGGING, new Vector3i(1, 63, 1), BlockFace.UP, 5)),
                client("PLAYER_DIGGING (release)", PacketType.Play.Client.PLAYER_DIGGING, () -> new WrapperPlayClientPlayerDigging(
                        DiggingAction.RELEASE_USE_ITEM, new Vector3i(0, 0, 0), BlockFace.DOWN, 0)),
                client("CLICK_WINDOW", PacketType.Play.Client.CLICK_WINDOW, () -> new WrapperPlayClientClickWindow(3, 1, 5, 0,
                        WrapperPlayClientClickWindow.WindowClickType.PICKUP, Map.of(), Optional.empty())),
                client("USE_ITEM", PacketType.Play.Client.USE_ITEM, () -> new WrapperPlayClientUseItem(InteractionHand.MAIN_HAND, 6)),
                client("PLUGIN_MESSAGE", PacketType.Play.Client.PLUGIN_MESSAGE, () -> new WrapperPlayClientPluginMessage(
                        new ResourceLocation("minecraft", "brand"), brand("vanilla"))),
                client("ENTITY_ACTION", PacketType.Play.Client.ENTITY_ACTION, () -> new WrapperPlayClientEntityAction(
                        SELF, WrapperPlayClientEntityAction.Action.START_SPRINTING, 0)),
                client("PLAYER_INPUT", PacketType.Play.Client.PLAYER_INPUT,
                        () -> new WrapperPlayClientPlayerInput(true, false, false, false, false, true, true)),
                client("VEHICLE_MOVE", PacketType.Play.Client.VEHICLE_MOVE,
                        () -> new WrapperPlayClientVehicleMove(new Vector3d(1, 64, 1), 0, 0, true)),
                client("PLAYER_ABILITIES (client)", PacketType.Play.Client.PLAYER_ABILITIES,
                        () -> new WrapperPlayClientPlayerAbilities(false)),
                client("CLOSE_WINDOW (client)", PacketType.Play.Client.CLOSE_WINDOW, () -> new WrapperPlayClientCloseWindow(3)),
                client("PLAYER_LOADED", PacketType.Play.Client.PLAYER_LOADED, WrapperPlayClientPlayerLoaded::new),
                server("SET_PASSENGERS", PacketType.Play.Server.SET_PASSENGERS,
                        () -> new WrapperPlayServerSetPassengers(OTHER, new int[]{SELF})),
                server("DEATH_COMBAT_EVENT", PacketType.Play.Server.DEATH_COMBAT_EVENT,
                        () -> new WrapperPlayServerDeathCombatEvent(SELF, null, Component.text("died"))),
                server("UPDATE_HEALTH", PacketType.Play.Server.UPDATE_HEALTH, () -> new WrapperPlayServerUpdateHealth(0, 20, 5)));
    }

    private static Case server(String name, PacketTypeCommon type, Supplier<PacketWrapper<?>> wrapper) {
        return new Case(name, true, type, wrapper);
    }

    private static Case client(String name, PacketTypeCommon type, Supplier<PacketWrapper<?>> wrapper) {
        return new Case(name, false, type, wrapper);
    }

    private static Case config(String name, PacketTypeCommon type, Supplier<PacketWrapper<?>> wrapper) {
        return new Case(name, false, type, wrapper);
    }

    private static byte[] brand(String brand) {
        byte[] text = brand.getBytes(StandardCharsets.UTF_8);
        byte[] out = new byte[text.length + 1];
        out[0] = (byte) text.length;
        System.arraycopy(text, 0, out, 1, text.length);
        return out;
    }

    private Log recordingLog() {
        return new Log() {
            @Override
            public void info(String message) {
            }

            @Override
            public void warn(String message) {
                warnings.add(message);
            }

            @Override
            public void warn(String message, Throwable error) {
                StringBuilder trace = new StringBuilder(message).append(": ").append(error);
                for (StackTraceElement element : error.getStackTrace()) {
                    trace.append("\n  at ").append(element);
                    if (trace.length() > 3_000) {
                        break;
                    }
                }
                warnings.add(trace.toString());
            }

            @Override
            public void debug(String message) {
            }
        };
    }

}
