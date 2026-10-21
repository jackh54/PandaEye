package dev.pandascript.pandaeye.paper.packet;

import java.nio.charset.StandardCharsets;

/** Decoding of the plugin message payloads PandaEye reads. */
public final class PluginMessages {

    public static final String BRAND = "minecraft:brand";
    static final int MAX_BRAND = 64;

    private PluginMessages() {
    }

    /**
     * The brand string (VarInt length + UTF-8), control characters stripped, at most 64 characters; empty when
     * malformed.
     */
    public static String brand(byte[] data) {
        if (data == null) {
            return "";
        }
        int length = 0;
        int shift = 0;
        int index = 0;
        while (index < data.length && index < 5) {
            byte b = data[index++];
            length |= (b & 0x7f) << shift;
            shift += 7;
            if ((b & 0x80) == 0) {
                break;
            }
        }
        if (length < 0 || index + length > data.length) {
            return "";
        }
        String raw = new String(data, index, length, StandardCharsets.UTF_8);
        StringBuilder clean = new StringBuilder(Math.min(raw.length(), MAX_BRAND));
        for (int i = 0; i < raw.length() && clean.length() < MAX_BRAND; i++) {
            char c = raw.charAt(i);
            if (c >= 0x20 && c != 0x7f && !(c >= 0x80 && c < 0xa0) && c != '§') {
                clean.append(c);
            }
        }
        return clean.toString();
    }
}
