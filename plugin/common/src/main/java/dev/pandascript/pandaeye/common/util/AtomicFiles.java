package dev.pandascript.pandaeye.common.util;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.AtomicMoveNotSupportedException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.nio.file.attribute.PosixFilePermission;
import java.util.EnumSet;
import java.util.Properties;
import java.util.Set;

/** Small-file helpers: atomic replace, owner-only permissions, properties. */
public final class AtomicFiles {

    private static final Set<PosixFilePermission> OWNER_ONLY =
            EnumSet.of(PosixFilePermission.OWNER_READ, PosixFilePermission.OWNER_WRITE);

    private AtomicFiles() {
    }

    /** Writes {@code content} to a sibling temp file and moves it over {@code target}. */
    public static void write(Path target, byte[] content, boolean ownerOnly) throws IOException {
        Path dir = target.toAbsolutePath().getParent();
        Files.createDirectories(dir);
        Path tmp = dir.resolve(target.getFileName() + ".tmp");
        Files.write(tmp, content);
        if (ownerOnly) {
            restrict(tmp);
        }
        try {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
        } catch (AtomicMoveNotSupportedException e) {
            Files.move(tmp, target, StandardCopyOption.REPLACE_EXISTING);
        }
        if (ownerOnly) {
            restrict(target);
        }
    }

    public static void writeString(Path target, String content, boolean ownerOnly) throws IOException {
        write(target, content.getBytes(StandardCharsets.UTF_8), ownerOnly);
    }

    /** Sets 0600 where the file system supports POSIX permissions; a no-op elsewhere. */
    public static void restrict(Path file) throws IOException {
        if (Files.getFileStore(file).supportsFileAttributeView("posix")) {
            Files.setPosixFilePermissions(file, OWNER_ONLY);
        }
    }

    public static Properties readProperties(Path file) throws IOException {
        Properties properties = new Properties();
        if (Files.isRegularFile(file)) {
            try (var reader = Files.newBufferedReader(file, StandardCharsets.UTF_8)) {
                properties.load(reader);
            }
        }
        return properties;
    }

    public static void writeProperties(Path file, Properties properties, String comment) throws IOException {
        var out = new java.io.StringWriter();
        properties.store(out, comment);
        writeString(file, out.toString(), false);
    }
}
