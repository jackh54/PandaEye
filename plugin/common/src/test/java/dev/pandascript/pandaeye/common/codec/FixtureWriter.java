package dev.pandascript.pandaeye.common.codec;

import com.google.protobuf.ByteString;
import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.Category;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.ContextMove;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.GameMode;
import dev.pandascript.pandaeye.protocol.v1.Hello;
import dev.pandascript.pandaeye.protocol.v1.LocalFlag;
import dev.pandascript.pandaeye.protocol.v1.MoveSample;
import dev.pandascript.pandaeye.protocol.v1.Platform;
import dev.pandascript.pandaeye.protocol.v1.PlayerJoin;


import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;

/**
 * Generates the cross-language codec fixtures in {@code test/fixtures} ({@code gradle :common:writeFixtures}).
 * The content is deterministic; {@link CodecFixturesTest} fails if the committed files drift from it.
 */
public final class FixtureWriter {

    static final byte[] PLAYER = HexFormat.of().parseHex("0123456789abcdef0123456789abcdef");

    record Fixture(String name, String description, ClientFrame frame) {
    }

    static List<Fixture> fixtures() {
        return List.of(
                new Fixture("hello", "Hello for server id 3f1c2b4a-0000-4000-8000-000000000001, protocol 1, shadow mode",
                        ClientFrame.newBuilder().setHello(Hello.newBuilder()
                                .setProtocolVersion(1)
                                .setLicenseKey("FIXTURE-LICENSE")
                                .setServerId("3f1c2b4a-0000-4000-8000-000000000001")
                                .setServerName("fixture")
                                .setPlatform(Platform.PLATFORM_FOLIA)
                                .setPlatformVersion("fixture-platform")
                                .setPluginVersion("0.1.0")
                                .setMinecraftVersion("1.21.11")
                                .setShadowMode(true)
                                .setClientTimeUnixMs(1_760_000_000_000L)
                                .addAllCapabilities(List.of("tick_end", "env_sampling", "target_history", "probe"))
                                .setOnlinePlayers(3)
                                .setInstanceId("3f1c2b4a-0000-4000-8000-0000000000ff")).build()),
                new Fixture("batch_small", "Batch seq 1 with a PlayerJoin and one MoveSample (below the 1 KiB threshold)",
                        ClientFrame.newBuilder().setBatch(Batch.newBuilder()
                                .setSeq(1).setSentUnixMs(1_760_000_000_050L)
                                .addEvents(Event.newBuilder().setPlayer(ByteString.copyFrom(PLAYER))
                                        .setTimeUs(1_760_000_000_000_000L)
                                        .setJoin(PlayerJoin.newBuilder().setName("Fixture_1").setProtocolVersion(774)
                                                .setClientVersion("1.21.11").setBrand("vanilla").setEntityId(42)
                                                .setGameMode(GameMode.GAME_MODE_SURVIVAL)
                                                .setSession("3f1c2b4a-0000-4000-8000-0000000000aa")))
                                .addEvents(Event.newBuilder().setPlayer(ByteString.copyFrom(PLAYER))
                                        .setTimeUs(1_760_000_000_050_000L).setTick(1)
                                        .setMove(move(0)))).build()),
                new Fixture("batch_moves", "Batch seq 2, replayed, 200 MoveSamples (x = i * 0.25, tick = i + 1)",
                        ClientFrame.newBuilder().setBatch(moves()).build()),
                new Fixture("batch_flag", "Batch seq 3 with a LocalFlag (movement.speed) carrying data and 40 context moves",
                        ClientFrame.newBuilder().setBatch(Batch.newBuilder()
                                .setSeq(3).setSentUnixMs(1_760_000_001_000L)
                                .addEvents(Event.newBuilder().setPlayer(ByteString.copyFrom(PLAYER))
                                        .setTimeUs(1_760_000_001_000_000L).setTick(400)
                                        .setFlag(flag()))).build()));
    }

    public static void main(String[] args) throws Exception {
        Path dir = Path.of(args[0]);
        Files.createDirectories(dir);
        FrameCodec codec = new FrameCodec(new TestLog());
        StringBuilder manifest = new StringBuilder("[\n");
        List<Fixture> all = fixtures();
        for (int i = 0; i < all.size(); i++) {
            Fixture fixture = all.get(i);
            byte[] message = fixture.frame().toByteArray();
            Files.write(dir.resolve(fixture.name() + ".pb"), message);
            Files.write(dir.resolve(fixture.name() + ".raw.bin"), codec.encodeWith(FrameCodec.CODEC_RAW, message));
            Files.write(dir.resolve(fixture.name() + ".zstd.bin"), codec.encodeWith(FrameCodec.CODEC_ZSTD, message));
            Files.write(dir.resolve(fixture.name() + ".deflate.bin"), codec.encodeWith(FrameCodec.CODEC_DEFLATE, message));
            manifest.append("  {\"name\": \"").append(fixture.name())
                    .append("\", \"kind\": \"").append(fixture.frame().getKindCase().name().toLowerCase())
                    .append("\", \"bytes\": ").append(message.length)
                    .append(", \"sha256\": \"").append(sha256(message)).append("\"")
                    .append(fixture.frame().hasBatch() ? ", \"seq\": " + fixture.frame().getBatch().getSeq()
                            + ", \"events\": " + fixture.frame().getBatch().getEventsCount() : "")
                    .append(", \"description\": \"").append(fixture.description()).append("\"}")
                    .append(i + 1 < all.size() ? ",\n" : "\n");
        }
        manifest.append("]\n");
        Files.writeString(dir.resolve("manifest.json"), manifest.toString(), StandardCharsets.UTF_8);
    }

    static String sha256(byte[] bytes) throws Exception {
        return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(bytes));
    }

    private static Batch moves() {
        Batch.Builder batch = Batch.newBuilder().setSeq(2).setSentUnixMs(1_760_000_000_500L).setReplayed(true);
        for (int i = 0; i < 200; i++) {
            batch.addEvents(Event.newBuilder().setPlayer(ByteString.copyFrom(PLAYER))
                    .setTimeUs(1_760_000_000_000_000L + i * 50_000L).setTick(i + 1).setMove(move(i)));
        }
        return batch.build();
    }

    private static MoveSample move(int i) {
        return MoveSample.newBuilder().setX(i * 0.25).setY(64).setZ(-12.5).setYaw(90.5f).setPitch(-10)
                .setOnGround(true).setHasPos(true).setHasRot(i % 2 == 0).setFlags(1).setTicksSinceLast(1)
                .setFocus(true).setPacketsSinceLast(1).build();
    }

    private static LocalFlag flag() {
        LocalFlag.Builder flag = LocalFlag.newBuilder()
                .setCheckId("movement.speed").setCategory(Category.CATEGORY_MOVEMENT)
                .setVl(3).setSeverity(0.5).setConfidence(0.75).setInfo("excess 0.150")
                .putAllData(Map.of("excess", 0.15))
                .putData("bound", 0.31)
                .setConfirmed(true);
        for (int i = 0; i < 40; i++) {
            flag.addContext(ContextMove.newBuilder().setTimeUs(1_760_000_000_000_000L + i * 50_000L)
                    .setTick(360 + i).setMove(move(i)));
        }
        return flag.build();
    }

}
