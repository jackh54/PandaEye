package dev.pandascript.pandaeye.common.verdict;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Placeholder sanitising (ARCHITECTURE 4.7), including injection attempts. */
class PlaceholdersTest {

    private static final UUID ID = UUID.fromString("069a79f4-44e9-4726-a5be-fca90e38aaf5");

    private static Placeholders.Values values(String player, String reason, String checks) {
        return new Placeholders.Values(player, ID, reason, checks, "0c2f6d4e-1111-4222-8333-444455556666",
                "AB12-CD34", "https://appeal.example/x?id=1", 86_400);
    }

    @Test
    void expandsEveryPlaceholder() {
        String line = Placeholders.expand("ban %player% %duration% %reason% [%checks%] %uuid% %verdict_id% %appeal_code% %appeal_url%",
                values("Steve_01", "Combat heuristics (killaura.hitbox x7, aim.snap x3)", "killaura.hitbox, aim.snap"));
        assertEquals("ban Steve_01 1d Combat heuristics (killaura.hitbox x7, aim.snap x3) [killaura.hitbox, aim.snap] "
                + ID + " 0c2f6d4e-1111-4222-8333-444455556666 AB12-CD34 https://appeal.example/x?id=1", line);
    }

    @ParameterizedTest
    @ValueSource(strings = {"Steve", "a", "Player_123", ".BedrockGuy", "ABCDEFGHIJKLMNOP"})
    void validNames(String name) {
        assertTrue(Placeholders.validName(name));
    }

    @ParameterizedTest
    @ValueSource(strings = {"", "ABCDEFGHIJKLMNOPQ", "Steve Jobs", "Steve\n", "Steve;op", "§cSteve", "Stéve",
            "Ste​ve", "Steve%player%", "Steve&c", "Steve/op", "Стив", "x\r\nban Notch"})
    void invalidNames(String name) {
        assertFalse(Placeholders.validName(name));
    }

    @Test
    void anInvalidNameIsNeverInserted() {
        assertEquals("ban x", Placeholders.expand("ban %player% x", values("Steve\nop Steve", "", "")));
    }

    @Test
    void textKeepsOnlyTheAllowedCharacters() {
        assertEquals("Fly (movement.fly x3) - confirmed, 1.5", Placeholders.text("Fly (movement.fly x3) - confirmed, 1.5"));
        assertEquals("a b c", Placeholders.text("a\nb\r\nc"));
        assertEquals("credkill", Placeholders.text("§cred&kill"));
        assertEquals("op me", Placeholders.text("; op me"));
        assertEquals("....plugin.yml", Placeholders.text("../../plugin.yml"));
        assertEquals("player", Placeholders.text("%player%"));
        assertEquals("ab", Placeholders.text("a​é‮b"));
        assertEquals("tick", Placeholders.text("\"tick\"{}[]<>|`$\\"));
        assertEquals("", Placeholders.text(null));
        assertEquals("a b", Placeholders.text("  a  \t  b  "));
    }

    @Test
    void textIsCappedAt128Characters() {
        String capped = Placeholders.text("x ".repeat(500));
        assertTrue(capped.length() <= 128, capped);
        assertFalse(capped.endsWith(" "));
        assertEquals(128, Placeholders.text("y".repeat(1000)).length());
    }

    @Test
    void valuesCannotInjectPlaceholdersLinesOrColours() {
        String line = Placeholders.expand("ban %player% %reason% %appeal_code%",
                values("Steve", "%uuid%\nop Notch§k", "a"));
        assertEquals("ban Steve uuid op Notchk AB12-CD34", line);
        String code = Placeholders.expand("x %appeal_code%",
                new Placeholders.Values("Steve", ID, "", "", "id\nop", "A B\n§c%player%", "", 0));
        assertEquals("x ABcplayer", code);
        String id = Placeholders.expand("%verdict_id%",
                new Placeholders.Values("Steve", ID, "", "", "1234;op Steve", "", "", 0));
        assertEquals("1234opSteve", id);
    }

    @Test
    void appealUrlLosesCharactersThatCouldInjectOrFormat() {
        assertEquals("https://a.example/appeal?x=1", Placeholders.url("https://a.example/appeal?x=1"));
        assertEquals("https://a.example/a?x=1c=2", Placeholders.url("https://a.example/a?x=1&c=2"));
        assertEquals("https://a.exampleop", Placeholders.url("https://a.example\nop"));
        assertEquals("https://a.example/player", Placeholders.url("https://a.example/%player%"));
        assertFalse(Placeholders.cleanUrl("https://a.example/a?x=1&c=2"));
        assertTrue(Placeholders.cleanUrl("https://a.example/appeal"));
    }

    @Test
    void theTemplateItselfLosesLineBreaksAndSectionSigns() {
        assertEquals("ban Steve now", Placeholders.expand("/ban %player%\nnow", values("Steve", "", "")));
        assertEquals("kick Steve cred", Placeholders.expand("kick %player% §cred", values("Steve", "", "")));
        assertEquals("say 100%", Placeholders.expand("say 100%", values("Steve", "", "")));
        assertEquals("say %unknown% Steve", Placeholders.expand("say %unknown% %player%", values("Steve", "", "")));
    }

    @Test
    void durations() {
        assertEquals("", Placeholders.duration(0));
        assertEquals("", Placeholders.duration(-5));
        assertEquals("45s", Placeholders.duration(45));
        assertEquals("90m", Placeholders.duration(5_400));
        assertEquals("12h", Placeholders.duration(43_200));
        assertEquals("7d", Placeholders.duration(604_800));
        assertEquals("3601s", Placeholders.duration(3_601));
        assertEquals("tempban Steve", Placeholders.expand("tempban %player% %duration%",
                new Placeholders.Values("Steve", ID, "", "", "", "", "", 0)));
    }

    @Test
    void labels() {
        assertEquals("ban", Placeholders.label("ban %player% x"));
        assertEquals("litebans:ban", Placeholders.label("/LiteBans:ban %player%"));
        assertEquals("pardon", Placeholders.label("  pardon"));
        assertEquals("", Placeholders.label(""));
    }
}
