package dev.pandascript.pandaeye.velocity;

import com.google.gson.Gson;
import com.velocitypowered.api.plugin.ap.SerializedPluginDescription;
import org.junit.jupiter.api.Test;

import java.io.InputStreamReader;
import java.io.Reader;
import java.nio.charset.StandardCharsets;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The built velocity-plugin.json, read the way Velocity's plugin loader reads it. */
class PluginDescriptionTest {

    @Test
    void theDescriptionNamesItsAuthorAndMainClass() throws Exception {
        SerializedPluginDescription description;
        try (Reader reader = new InputStreamReader(getClass().getResourceAsStream("/velocity-plugin.json"),
                StandardCharsets.UTF_8)) {
            description = new Gson().fromJson(reader, SerializedPluginDescription.class);
        }
        assertEquals("pandaeye", description.getId());
        assertTrue(SerializedPluginDescription.ID_PATTERN.matcher(description.getId()).matches());
        assertEquals(List.of("PandaScript"), description.getAuthors(), "Velocity logs 'by <authors>'");
        assertEquals(PandaEyeVelocity.class.getName(), description.getMain());
        assertFalse(description.getVersion().contains("${"), "version expanded by processResources");
    }
}
