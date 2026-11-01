package dev.pandascript.pandaeye.paper.platform;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Which jar entries the preloader loads: classes only, never one that needs an optional plugin. */
class ClassPreloaderTest {

    @Test
    void classesBuiltOnAnOptionalPluginAreSkipped() {
        assertTrue(ClassPreloader.preloadable("dev/pandascript/pandaeye/common/litebans/LiteBansLabels.class"));
        assertTrue(ClassPreloader.preloadable("dev/pandascript/pandaeye/common/litebans/BanClassifier$Label.class"));
        assertTrue(ClassPreloader.preloadable("dev/pandascript/pandaeye/paper/runtime/Runtime.class"));
        assertFalse(ClassPreloader.preloadable("dev/pandascript/pandaeye/common/litebans/LiteBansHook.class"),
                "extends LiteBans' Events.Listener: cannot load on a server without LiteBans");
        assertFalse(ClassPreloader.preloadable("dev/pandascript/pandaeye/common/litebans/LiteBansHook$1.class"));
        assertFalse(ClassPreloader.preloadable("META-INF/versions/9/module-info.class"));
        assertFalse(ClassPreloader.preloadable("config.yml"));
    }
}
