package dev.pandascript.pandaeye.paper.packet;

import java.util.Set;

/** Ordinal-indexed lookup tables for packet type enums (one array read per packet). */
final class PacketTypes {

    private PacketTypes() {
    }

    static <E extends Enum<E>> boolean[] table(E[] all, Set<E> handled) {
        boolean[] table = new boolean[all.length];
        for (E type : handled) {
            table[type.ordinal()] = true;
        }
        return table;
    }
}
