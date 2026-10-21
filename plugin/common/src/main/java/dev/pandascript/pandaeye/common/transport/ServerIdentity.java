package dev.pandascript.pandaeye.common.transport;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.util.AtomicFiles;
import dev.pandascript.pandaeye.common.util.Uuids;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.UUID;

/**
 * The stable server identity: {@code server-id} (canonical lowercase UUID, generated on first start) and the
 * {@code server-secret} the cloud issues in the first Welcome. Both files are owner-only (0600).
 */
public final class ServerIdentity {

    public static final String SERVER_ID_FILE = "server-id";
    public static final String SERVER_SECRET_FILE = "server-secret";

    private final Path secretFile;
    private final String serverId;
    private volatile String secret;

    private ServerIdentity(Path secretFile, String serverId, String secret) {
        this.secretFile = secretFile;
        this.serverId = serverId;
        this.secret = secret;
    }

    public static ServerIdentity load(Path dataDir, Log log) throws IOException {
        Files.createDirectories(dataDir);
        Path idFile = dataDir.resolve(SERVER_ID_FILE);
        Path secretFile = dataDir.resolve(SERVER_SECRET_FILE);
        String id = Files.isRegularFile(idFile) ? Files.readString(idFile, StandardCharsets.UTF_8).trim() : "";
        if (!Uuids.isCanonical(id)) {
            if (!id.isEmpty()) {
                log.warn("server-id file does not hold a canonical lowercase UUID; generating a new server id");
            }
            id = UUID.randomUUID().toString();
            AtomicFiles.writeString(idFile, id + "\n", true);
            Files.deleteIfExists(secretFile);
        } else {
            AtomicFiles.restrict(idFile);
        }
        String secret = "";
        if (Files.isRegularFile(secretFile)) {
            AtomicFiles.restrict(secretFile);
            secret = Files.readString(secretFile, StandardCharsets.UTF_8).trim();
        }
        return new ServerIdentity(secretFile, id, secret);
    }

    public String serverId() {
        return serverId;
    }

    public String secret() {
        return secret;
    }

    void saveSecret(String newSecret) throws IOException {
        AtomicFiles.writeString(secretFile, newSecret + "\n", true);
        secret = newSecret;
    }
}
