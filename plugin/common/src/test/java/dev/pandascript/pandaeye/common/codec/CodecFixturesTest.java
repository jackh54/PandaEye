package dev.pandascript.pandaeye.common.codec;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import io.airlift.compress.zstd.ZstdDecompressor;
import org.junit.jupiter.api.Test;

import java.nio.file.Files;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The committed cross-language fixtures decode with this codec and match the generator. */
class CodecFixturesTest {

    private static final Path DIR = Path.of(System.getProperty("pandaeye.fixtures", "../../test/fixtures"));

    @Test
    void committedFixturesMatchTheGeneratorAndDecodeWithEveryCodec() throws Exception {
        assertTrue(Files.isDirectory(DIR), "missing " + DIR + "; run gradle :common:writeFixtures");
        FrameCodec codec = new FrameCodec(new TestLog());
        String manifest = Files.readString(DIR.resolve("manifest.json"));
        for (FixtureWriter.Fixture fixture : FixtureWriter.fixtures()) {
            byte[] expected = fixture.frame().toByteArray();
            byte[] committed = Files.readAllBytes(DIR.resolve(fixture.name() + ".pb"));
            assertArrayEquals(expected, committed, fixture.name() + ".pb is stale; run gradle :common:writeFixtures");
            assertEquals(fixture.frame(), ClientFrame.parseFrom(committed));
            assertTrue(manifest.contains(FixtureWriter.sha256(expected)), "manifest.json is stale");
            byte[] zstd = Files.readAllBytes(DIR.resolve(fixture.name() + ".zstd.bin"));
            assertEquals(expected.length, ZstdDecompressor.getDecompressedSize(zstd, 1, zstd.length - 1),
                    "zstd fixtures carry the content size");
            for (String codecName : new String[]{"raw", "zstd", "deflate"}) {
                byte[] frame = Files.readAllBytes(DIR.resolve(fixture.name() + "." + codecName + ".bin"));
                assertArrayEquals(expected, codec.decode(frame), fixture.name() + "." + codecName);
            }
        }
    }
}
