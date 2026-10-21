package dev.pandascript.pandaeye.paper.packet;

import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class PluginMessagesTest {

    private static byte[] brand(String text) {
        byte[] body = text.getBytes(StandardCharsets.UTF_8);
        byte[] data = new byte[body.length + 1];
        data[0] = (byte) body.length;
        System.arraycopy(body, 0, data, 1, body.length);
        return data;
    }

    @Test
    void decodesTheBrand() {
        assertEquals("vanilla", PluginMessages.brand(brand("vanilla")));
        assertEquals("fabric", PluginMessages.brand(brand("fabric")));
    }

    @Test
    void stripsControlAndFormattingCharactersAndCapsTheLength() {
        assertEquals("evilcbrand", PluginMessages.brand(brand("evil\n\u0007§cbrand")));
        assertEquals(PluginMessages.MAX_BRAND, PluginMessages.brand(brand("x".repeat(100))).length());
    }

    @Test
    void malformedPayloadsAreEmpty() {
        assertEquals("", PluginMessages.brand(null));
        assertEquals("", PluginMessages.brand(new byte[0]));
        assertEquals("", PluginMessages.brand(new byte[] {10, 'a'}));
        assertEquals("", PluginMessages.brand(new byte[] {(byte) 0xff, (byte) 0xff, (byte) 0xff, (byte) 0xff, 0x0f}));
    }

    @Test
    void packetCostReportsAverageAndP99() {
        PacketCost cost = new PacketCost();
        assertEquals(0, cost.summary().packets());
        for (int i = 0; i < 99; i++) {
            cost.record(2_000);
        }
        cost.record(1_000_000);
        PacketCost.Summary summary = cost.summary();
        assertEquals(100, summary.packets());
        assertEquals((99 * 2_000 + 1_000_000) / 100.0 / 1_000.0, summary.averageMicros(), 1e-9);
        assertTrue(summary.p99Micros() >= 2.0 && summary.p99Micros() <= 2.6, "p99 " + summary.p99Micros());
        cost.reset();
        assertEquals(0, cost.summary().packets());
    }
}
