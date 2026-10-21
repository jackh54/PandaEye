package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.event.PacketReceiveEvent;
import com.github.retrooper.packetevents.protocol.packettype.PacketType;
import com.github.retrooper.packetevents.protocol.player.DiggingAction;
import com.github.retrooper.packetevents.protocol.world.Location;
import com.github.retrooper.packetevents.util.Vector3f;
import com.github.retrooper.packetevents.util.Vector3i;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientAttack;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientClickWindow;
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
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPluginMessage;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientPong;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientTeleportConfirm;
import com.github.retrooper.packetevents.wrapper.play.client.WrapperPlayClientUseItem;
import dev.pandascript.pandaeye.common.proxy.ProxySessionToken;
import dev.pandascript.pandaeye.paper.check.CheckInput;
import dev.pandascript.pandaeye.paper.session.Session;
import dev.pandascript.pandaeye.paper.state.PacketProcessor;

import java.util.EnumSet;

import static dev.pandascript.pandaeye.paper.packet.Wrappers.read;

/** Decodes server-bound PLAY packets into {@link PacketProcessor} calls (netty thread). */
final class ClientPacketReader {

    /** The types {@link #handle} reads; keep in sync with its switch. Everything else is ignored unseen. */
    private static final boolean[] HANDLED = PacketTypes.table(PacketType.Play.Client.values(), EnumSet.of(
            PacketType.Play.Client.PLAYER_FLYING, PacketType.Play.Client.PLAYER_POSITION,
            PacketType.Play.Client.PLAYER_ROTATION, PacketType.Play.Client.PLAYER_POSITION_AND_ROTATION,
            PacketType.Play.Client.CLIENT_TICK_END, PacketType.Play.Client.TELEPORT_CONFIRM,
            PacketType.Play.Client.KEEP_ALIVE, PacketType.Play.Client.PONG, PacketType.Play.Client.HELD_ITEM_CHANGE,
            PacketType.Play.Client.INTERACT_ENTITY, PacketType.Play.Client.ATTACK, PacketType.Play.Client.ANIMATION,
            PacketType.Play.Client.PLAYER_BLOCK_PLACEMENT, PacketType.Play.Client.PLAYER_DIGGING,
            PacketType.Play.Client.CLICK_WINDOW, PacketType.Play.Client.USE_ITEM,
            PacketType.Play.Client.PLUGIN_MESSAGE, PacketType.Play.Client.ENTITY_ACTION,
            PacketType.Play.Client.PLAYER_INPUT, PacketType.Play.Client.VEHICLE_MOVE,
            PacketType.Play.Client.STEER_VEHICLE, PacketType.Play.Client.STEER_BOAT,
            PacketType.Play.Client.PLAYER_ABILITIES, PacketType.Play.Client.CLOSE_WINDOW,
            PacketType.Play.Client.PLAYER_LOADED));

    private final ProxySessionChannel proxySessions;

    static boolean handles(PacketType.Play.Client type) {
        return HANDLED[type.ordinal()];
    }

    ClientPacketReader(ProxySessionChannel proxySessions) {
        this.proxySessions = proxySessions;
    }

    void handle(PacketReceiveEvent event, PacketType.Play.Client type, Session session, long now) {
        PacketProcessor processor = session.processor();
        switch (type) {
            case PLAYER_FLYING, PLAYER_POSITION, PLAYER_ROTATION, PLAYER_POSITION_AND_ROTATION -> {
                WrapperPlayClientPlayerFlying flying = read(event, WrapperPlayClientPlayerFlying.class,
                        WrapperPlayClientPlayerFlying::new);
                Location location = flying.getLocation();
                processor.flying(location.getX(), location.getY(), location.getZ(), location.getYaw(),
                        location.getPitch(), flying.hasPositionChanged(), flying.hasRotationChanged(),
                        flying.isOnGround(), flying.isHorizontalCollision(), event.isCancelled(), now);
            }
            case CLIENT_TICK_END -> processor.tickEnd(now);
            case TELEPORT_CONFIRM -> processor.teleportConfirm(read(event, WrapperPlayClientTeleportConfirm.class,
                    WrapperPlayClientTeleportConfirm::new).getTeleportId(), now);
            case KEEP_ALIVE -> processor.clientKeepAlive(read(event, WrapperPlayClientKeepAlive.class,
                    WrapperPlayClientKeepAlive::new).getId(), now);
            case PONG -> processor.clientPong(read(event, WrapperPlayClientPong.class,
                    WrapperPlayClientPong::new).getId(), now);
            case HELD_ITEM_CHANGE -> processor.clientHeldItem(read(event, WrapperPlayClientHeldItemChange.class,
                    WrapperPlayClientHeldItemChange::new).getSlot(), now);
            case INTERACT_ENTITY -> {
                WrapperPlayClientInteractEntity interact = read(event, WrapperPlayClientInteractEntity.class,
                        WrapperPlayClientInteractEntity::new);
                processor.interact(interact.getEntityId(),
                        interact.getAction() == WrapperPlayClientInteractEntity.InteractAction.ATTACK, now);
            }
            case ATTACK -> processor.interact(read(event, WrapperPlayClientAttack.class,
                    WrapperPlayClientAttack::new).getEntityId(), true, now);
            case ANIMATION -> processor.animation(RawPeek.leadingVarInt(event.getByteBuf()), now);
            case PLAYER_BLOCK_PLACEMENT -> {
                WrapperPlayClientPlayerBlockPlacement place = read(event, WrapperPlayClientPlayerBlockPlacement.class,
                        WrapperPlayClientPlayerBlockPlacement::new);
                Vector3i block = place.getBlockPosition();
                Vector3f cursor = place.getCursorPosition();
                processor.place(block.x, block.y, block.z, place.getFaceId(), cursor.x, cursor.y, cursor.z,
                        place.getHand().getId(), place.getInsideBlock().orElse(false), now);
            }
            case PLAYER_DIGGING -> {
                WrapperPlayClientPlayerDigging dig = read(event, WrapperPlayClientPlayerDigging.class,
                        WrapperPlayClientPlayerDigging::new);
                Vector3i block = dig.getBlockPosition();
                processor.dig(action(dig.getAction()), block.x, block.y, block.z, dig.getBlockFaceId(), now);
            }
            case CLICK_WINDOW -> {
                WrapperPlayClientClickWindow click = read(event, WrapperPlayClientClickWindow.class,
                        WrapperPlayClientClickWindow::new);
                processor.windowClick(click.getWindowId(), click.getSlot(), click.getButton(),
                        click.getWindowClickType().ordinal(), now);
            }
            case USE_ITEM -> processor.useItem(read(event, WrapperPlayClientUseItem.class,
                    WrapperPlayClientUseItem::new).getHand().getId(), now);
            case PLUGIN_MESSAGE -> pluginMessage(event, session, now);
            case ENTITY_ACTION -> entityAction(read(event, WrapperPlayClientEntityAction.class,
                    WrapperPlayClientEntityAction::new).getAction(), processor, now);
            case PLAYER_INPUT -> processor.sneaking(read(event, WrapperPlayClientPlayerInput.class,
                    WrapperPlayClientPlayerInput::new).isShift(), true);
            case VEHICLE_MOVE, STEER_VEHICLE, STEER_BOAT -> processor.vehicleSignal(now);
            case PLAYER_ABILITIES -> processor.clientAbilities(read(event, WrapperPlayClientPlayerAbilities.class,
                    WrapperPlayClientPlayerAbilities::new).isFlying(), now);
            case CLOSE_WINDOW -> processor.clientCloseWindow(read(event, WrapperPlayClientCloseWindow.class,
                    WrapperPlayClientCloseWindow::new).getWindowId(), now);
            case PLAYER_LOADED -> processor.playerLoaded();
            default -> {
            }
        }
    }

    private void pluginMessage(PacketReceiveEvent event, Session session, long now) {
        WrapperPlayClientPluginMessage message = read(event, WrapperPlayClientPluginMessage.class,
                WrapperPlayClientPluginMessage::new);
        String channel = message.getChannelName();
        if (PluginMessages.BRAND.equals(channel)) {
            session.processor().brand(PluginMessages.brand(message.getData()), now);
        } else if (ProxySessionToken.CHANNEL.equals(channel)) {
            proxySessions.received(session.uuid(), session, message.getData(), now);
        }
    }

    private static void entityAction(WrapperPlayClientEntityAction.Action action, PacketProcessor processor, long now) {
        if (action == WrapperPlayClientEntityAction.Action.START_SPRINTING) {
            processor.sprinting(true);
        } else if (action == WrapperPlayClientEntityAction.Action.STOP_SPRINTING) {
            processor.sprinting(false);
        } else if (action == WrapperPlayClientEntityAction.Action.START_SNEAKING) {
            processor.sneaking(true, false);
        } else if (action == WrapperPlayClientEntityAction.Action.STOP_SNEAKING) {
            processor.sneaking(false, false);
        } else if (action == WrapperPlayClientEntityAction.Action.START_FLYING_WITH_ELYTRA) {
            processor.startElytra(now);
        }
    }

    private static CheckInput.DigAction action(DiggingAction action) {
        return switch (action) {
            case START_DIGGING -> CheckInput.DigAction.START;
            case CANCELLED_DIGGING -> CheckInput.DigAction.CANCEL;
            case FINISHED_DIGGING -> CheckInput.DigAction.FINISH;
            case RELEASE_USE_ITEM -> CheckInput.DigAction.RELEASE_USE_ITEM;
            case STAB -> CheckInput.DigAction.STAB;
            default -> CheckInput.DigAction.OTHER;
        };
    }
}
