package dev.pandascript.pandaeye.paper.platform;

import java.io.IOException;
import java.net.URISyntaxException;
import java.nio.file.Path;
import java.util.Enumeration;
import java.util.jar.JarEntry;
import java.util.jar.JarFile;

/**
 * Loads and initialises every class of the plugin jar during enable, so netty and region threads never
 * trigger class loading (which fails once a hot unload has closed the class loader). Returns counts for the
 * enable log.
 */
public final class ClassPreloader {

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
                if (!name.endsWith(".class") || name.contains("-") || name.startsWith("META-INF")) {
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
}
