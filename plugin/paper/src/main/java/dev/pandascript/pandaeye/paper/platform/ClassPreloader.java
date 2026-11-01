package dev.pandascript.pandaeye.paper.platform;

import java.io.IOException;
import java.net.URISyntaxException;
import java.nio.file.Path;
import java.util.Enumeration;
import java.util.List;
import java.util.jar.JarEntry;
import java.util.jar.JarFile;

/**
 * Loads and initialises every class of the plugin jar during enable, so netty and region threads never
 * trigger class loading (which fails once a hot unload has closed the class loader). Returns counts for the
 * enable log. Classes built on an optional plugin's API are skipped: they cannot load without that plugin, and
 * with it they are loaded during enable when the integration starts.
 */
public final class ClassPreloader {

    /** Jar entry prefixes of the classes that extend an optional plugin's API (LiteBans). */
    static final List<String> OPTIONAL = List.of("dev/pandascript/pandaeye/common/litebans/LiteBansHook");

    public record Result(int loaded, int failed) {
    }

    private ClassPreloader() {
    }

    public static Result preload(Class<?> anchor) {
        ClassLoader loader = anchor.getClassLoader();
        Path jar;
        try {
            jar = Path.of(anchor.getProtectionDomain().getCodeSource().getLocation().toURI());
        } catch (URISyntaxException | RuntimeException e) {
            return new Result(0, 0);
        }
        if (!jar.toString().endsWith(".jar")) {
            return new Result(0, 0);
        }
        int loaded = 0;
        int failed = 0;
        try (JarFile file = new JarFile(jar.toFile())) {
            Enumeration<JarEntry> entries = file.entries();
            while (entries.hasMoreElements()) {
                String name = entries.nextElement().getName();
                if (!preloadable(name)) {
                    continue;
                }
                try {
                    Class.forName(name.substring(0, name.length() - 6).replace('/', '.'), true, loader);
                    loaded++;
                } catch (Throwable t) {
                    failed++;
                }
            }
        } catch (IOException e) {
            return new Result(loaded, failed + 1);
        }
        return new Result(loaded, failed);
    }

    static boolean preloadable(String entry) {
        if (!entry.endsWith(".class") || entry.contains("-") || entry.startsWith("META-INF")) {
            return false;
        }
        for (String prefix : OPTIONAL) {
            if (entry.startsWith(prefix)) {
                return false;
            }
        }
        return true;
    }
}
