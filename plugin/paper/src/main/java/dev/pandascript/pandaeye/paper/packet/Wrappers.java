package dev.pandascript.pandaeye.paper.packet;

import com.github.retrooper.packetevents.event.ProtocolPacketEvent;
import com.github.retrooper.packetevents.netty.buffer.ByteBufHelper;
import com.github.retrooper.packetevents.wrapper.PacketWrapper;

import java.util.function.Function;

/**
 * Wrapper hygiene (ARCHITECTURE 4.2): the only way PandaEye reads a packet through a PacketEvents wrapper. If
 * another plugin already wrapped the packet with the same wrapper type, that wrapper is read (it is what will
 * be sent). Otherwise a fresh wrapper is decoded and the event's last-used wrapper and the buffer's reader
 * index are restored, so PacketEvents (with {@code reEncodeByDefault}) never re-encodes a packet because of
 * PandaEye. The returned wrapper must only be read: wrapper setters are forbidden (ArchUnit).
 */
public final class Wrappers {

    private Wrappers() {
    }

    public static <E extends ProtocolPacketEvent, W extends PacketWrapper<?>> W read(E event, Class<W> type,
                                                                                    Function<E, W> decoder) {
        PacketWrapper<?> previous = event.getLastUsedWrapper();
        if (type.isInstance(previous)) {
            return type.cast(previous);
        }
        Object buffer = event.getByteBuf();
        int readerIndex = ByteBufHelper.readerIndex(buffer);
        try {
            return decoder.apply(event);
        } finally {
            event.setLastUsedWrapper(previous);
            ByteBufHelper.readerIndex(buffer, readerIndex);
        }
    }
}
