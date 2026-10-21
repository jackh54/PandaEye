package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.event.PacketSendEvent;
import com.github.retrooper.packetevents.protocol.entity.data.EntityData;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.teleport.RelativeFlag;
import com.github.retrooper.packetevents.util.Vector3d;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerChangeGameState;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerCloseWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerDeathCombatEvent;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityEffect;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityMetadata;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerEntityVelocity;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerExplosion;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerHeldItemChange;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerKeepAlive;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerOpenHorseWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerOpenWindow;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPing;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerAbilities;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerPositionAndLook;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerPlayerRotation;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerRemoveEntityEffect;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerRespawn;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerTickingState;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerUpdateAttributes;
import com.github.retrooper.packetevents.wrapper.play.server.WrapperPlayServerUpdateHealth;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.Abilities;
import dev.pandascript.pandaeye.paper.state.AttributeMath;
import dev.pandascript.pandaeye.paper.state.AttributeTracker;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;
import dev.pandascript.pandaeye.paper.state.PacketState;
import dev.pandascript.pandaeye.paper.state.VelocityTracker;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import static dev.pandascript.pandaeye.paper.packet.Wrappers.read;

/**
 * Decodes client-bound PLAY packets into {@link PacketProcessor} calls (netty thread). Entity-scoped packets
 * are skipped by a raw peek of the leading entity id unless they concern the player's own entity; nothing
 * id-based is read before the session is seeded.
 */
final class ServerPacketReader {

    private static final int METADATA_ENTITY_FLAGS = 0;
    private static final int METADATA_LIVING_FLAGS = 8;

    void handle(PacketSendEvent event, PacketType.Play.Server type, Session session, long now) {
        PacketProcessor processor = session.processor();
        PacketState state = session.state();
        switch (type) {
            case RESPAWN -> {
                WrapperPlayServerRespawn respawn = read(event, WrapperPlayServerRespawn.class, WrapperPlayServerRespawn::new);
                processor.respawn(GameModes.of(respawn.getGameMode()), respawn.getWorldName().orElse(null), now);
            }
            case PLAYER_POSITION_AND_LOOK -> {
                WrapperPlayServerPlayerPositionAndLook teleport = read(event,
                        WrapperPlayServerPlayerPositionAndLook.class, WrapperPlayServerPlayerPositionAndLook::new);
                Vector3d delta = teleport.getDeltaMovement();
                processor.serverTeleport(teleport.getTeleportId(), teleport.getX(), teleport.getY(), teleport.getZ(),
                        teleport.getYaw(), teleport.getPitch(), teleport.isRelativeFlag(RelativeFlag.X),
                        teleport.isRelativeFlag(RelativeFlag.Y), teleport.isRelativeFlag(RelativeFlag.Z),
                        teleport.isRelativeFlag(RelativeFlag.YAW), teleport.isRelativeFlag(RelativeFlag.PITCH),
                        delta == null ? 0 : delta.x, delta == null ? 0 : delta.y, delta == null ? 0 : delta.z, now);
            }
            case PLAYER_ROTATION -> {
                WrapperPlayServerPlayerRotation rotation = read(event, WrapperPlayServerPlayerRotation.class,
                        WrapperPlayServerPlayerRotation::new);
                processor.serverRotation(rotation.getPitch(), rotation.isRelativePitch(), true);
            }
            case FACE_PLAYER -> processor.serverRotation(0, false, false);
            case ENTITY_VELOCITY -> {
                if (own(event, state)) {
                    Vector3d velocity = read(event, WrapperPlayServerEntityVelocity.class,
                            WrapperPlayServerEntityVelocity::new).getVelocity();
                    processor.velocity(velocity.x, velocity.y, velocity.z, VelocityTracker.Source.PACKET, now);
                }
            }
            case EXPLOSION -> {
                Vector3d knockback = read(event, WrapperPlayServerExplosion.class, WrapperPlayServerExplosion::new)
                        .getKnockback();
                if (knockback != null) {
                    processor.velocity(knockback.x, knockback.y, knockback.z, VelocityTracker.Source.EXPLOSION, now);
                }
            }
            case PLAYER_ABILITIES -> {
                WrapperPlayServerPlayerAbilities abilities = read(event, WrapperPlayServerPlayerAbilities.class,
                        WrapperPlayServerPlayerAbilities::new);
                processor.serverAbilities(new Abilities(abilities.isFlying(), abilities.isFlightAllowed(),
                        abilities.isInCreativeMode(), abilities.getFlySpeed()), now);
            }
            case CHANGE_GAME_STATE -> {
                WrapperPlayServerChangeGameState change = read(event, WrapperPlayServerChangeGameState.class,
                        WrapperPlayServerChangeGameState::new);
                if (change.getReason() == WrapperPlayServerChangeGameState.Reason.CHANGE_GAME_MODE) {
                    processor.gameMode(GameModes.ofId((int) change.getValue()), now);
                }
            }
            case ENTITY_EFFECT -> {
                if (own(event, state)) {
                    WrapperPlayServerEntityEffect effect = read(event, WrapperPlayServerEntityEffect.class,
                            WrapperPlayServerEntityEffect::new);
                    processor.effect(effect.getPotionType().getName().getKey(), effect.getEffectAmplifier(), now);
                }
            }
            case REMOVE_ENTITY_EFFECT -> {
                if (own(event, state)) {
                    processor.effectRemoved(read(event, WrapperPlayServerRemoveEntityEffect.class,
                            WrapperPlayServerRemoveEntityEffect::new).getPotionType().getName().getKey(), now);
                }
            }
            case UPDATE_ATTRIBUTES -> {
                if (own(event, state)) {
                    processor.attributes(attributes(read(event, WrapperPlayServerUpdateAttributes.class,
                            WrapperPlayServerUpdateAttributes::new)), now);
                }
            }
            case ENTITY_METADATA -> {
                if (own(event, state)) {
                    metadata(read(event, WrapperPlayServerEntityMetadata.class, WrapperPlayServerEntityMetadata::new),
                            processor, now);
                }
            }
            case UPDATE_HEALTH -> processor.health(read(event, WrapperPlayServerUpdateHealth.class,
                    WrapperPlayServerUpdateHealth::new).getHealth());
            case DEATH_COMBAT_EVENT -> {
                if (state.seeded() && read(event, WrapperPlayServerDeathCombatEvent.class,
                        WrapperPlayServerDeathCombatEvent::new).getPlayerId() == state.entityId()) {
                    processor.death();
                }
            }
            case TICKING_STATE -> {
                WrapperPlayServerTickingState ticking = read(event, WrapperPlayServerTickingState.class,
                        WrapperPlayServerTickingState::new);
                processor.tickingState(ticking.getTickRate(), ticking.isFrozen(), now);
            }
            case SET_PASSENGERS -> {
                if (state.seeded()) {
                    RawPeek.Passengers passengers = RawPeek.passengers(event.getByteBuf(), state.entityId());
                    if (passengers.includes() || passengers.vehicle() == state.vehicleId()) {
                        processor.passengers(passengers.vehicle(), passengers.includes(), now);
                    }
                }
            }
            case OPEN_WINDOW -> {
                WrapperPlayServerOpenWindow open = read(event, WrapperPlayServerOpenWindow.class,
                        WrapperPlayServerOpenWindow::new);
                processor.openWindow(open.getContainerId(), MenuTypes.key(open.getType()), now);
            }
            case OPEN_HORSE_WINDOW -> processor.openWindow(read(event, WrapperPlayServerOpenHorseWindow.class,
                    WrapperPlayServerOpenHorseWindow::new).getWindowId(), "minecraft:horse", now);
            case CLOSE_WINDOW -> processor.serverCloseWindow(read(event, WrapperPlayServerCloseWindow.class,
                    WrapperPlayServerCloseWindow::new).getWindowId(), now);
            case KEEP_ALIVE -> processor.serverKeepAlive(read(event, WrapperPlayServerKeepAlive.class,
                    WrapperPlayServerKeepAlive::new).getId(), now);
            case PING -> processor.serverPing(read(event, WrapperPlayServerPing.class, WrapperPlayServerPing::new)
                    .getId(), now);
            case HELD_ITEM_CHANGE -> processor.serverHeldItem(read(event, WrapperPlayServerHeldItemChange.class,
                    WrapperPlayServerHeldItemChange::new).getSlot());
            default -> {
            }
        }
    }

    /** True when the packet's leading entity id is the seeded player's own. */
    private static boolean own(PacketSendEvent event, PacketState state) {
        return state.seeded() && RawPeek.leadingVarInt(event.getByteBuf()) == state.entityId();
    }

    static Map<String, Double> attributes(WrapperPlayServerUpdateAttributes packet) {
        Map<String, Double> values = new HashMap<>();
        for (WrapperPlayServerUpdateAttributes.Property property : packet.getProperties()) {
            if (property.getAttribute() == null) {
                continue;
            }
            String key = AttributeTracker.normalise(property.getAttribute().getName().toString());
            if (key == null) {
                continue;
            }
            List<AttributeMath.Modifier> modifiers = new ArrayList<>();
            for (WrapperPlayServerUpdateAttributes.PropertyModifier modifier : property.getModifiers()) {
                if (modifier.getName() != null && AttributeMath.SPRINTING_MODIFIER.equals(modifier.getName().toString())) {
                    continue;
                }
                AttributeMath.Operation operation = switch (modifier.getOperation()) {
                    case ADDITION -> AttributeMath.Operation.ADD_VALUE;
                    case MULTIPLY_BASE -> AttributeMath.Operation.ADD_MULTIPLIED_BASE;
                    case MULTIPLY_TOTAL -> AttributeMath.Operation.ADD_MULTIPLIED_TOTAL;
                };
                modifiers.add(new AttributeMath.Modifier(operation, modifier.getAmount()));
            }
            values.put(key, AttributeMath.value(property.getValue(), modifiers));
        }
        return values;
    }

    private static void metadata(WrapperPlayServerEntityMetadata packet, PacketProcessor processor, long now) {
        Integer flags = null;
        Integer hands = null;
        for (EntityData<?> data : packet.getEntityMetadata()) {
            if (data.getValue() instanceof Byte value) {
                if (data.getIndex() == METADATA_ENTITY_FLAGS) {
                    flags = value & 0xff;
                } else if (data.getIndex() == METADATA_LIVING_FLAGS) {
                    hands = value & 0xff;
                }
            }
        }
        if (flags != null || hands != null) {
            processor.metadata(flags, hands, now);
        }
    }
}
