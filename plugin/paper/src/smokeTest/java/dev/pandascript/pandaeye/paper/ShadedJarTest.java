package dev.pandascript.pandaeye.paper;

import dev.pandascript.pandaeye.common.Log;
import dev.pandascript.pandaeye.common.codec.FrameCodec;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;
import dev.pandascript.pandaeye.protocol.v1.Event;
import dev.pandascript.pandaeye.protocol.v1.LocalFlag;
import dev.pandascript.pandaeye.protocol.v1.MoveSample;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.jar.JarFile;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** Runs on the shaded plugin jar only: protobuf-lite and zstd must work after relocation and minimisation. */
class ShadedJarTest {

    @Test
    void protobufAndZstdWorkInsideTheJar() throws Exception {
        Batch.Builder batch = Batch.newBuilder().setSeq(9).setReplayed(true);
        for (int i = 0; i < 500; i++) {
            batch.addEvents(Event.newBuilder().setTimeUs(i).setTick(i)
                    .setMove(MoveSample.newBuilder().setX(i * 0.25).setY(64).setHasPos(true)));
        }
        batch.addEvents(Event.newBuilder().setFlag(LocalFlag.newBuilder().setCheckId("movement.speed")
                .putData("excess", 0.2)));
        // build() is inherited from the (relocated) protobuf runtime, so it is called reflectively: this test
        // is compiled against the unshaded classes.
        ClientFrame.Builder builder = ClientFrame.newBuilder().setBatch(batch);
        ClientFrame frame = (ClientFrame) builder.getClass().getMethod("build").invoke(builder);
        byte[] message = frame.toByteArray();
        assertEquals(frame, ClientFrame.parseFrom(message));

        List<String> warnings = new ArrayList<>();
        FrameCodec codec = new FrameCodec(new Log() {
            @Override
            public void info(String text) {
            }

            @Override
            public void warn(String text) {
                warnings.add(text);
            }

            @Override
            public void warn(String text, Throwable error) {
                warnings.add(text + " " + error);
            }

            @Override
            public void debug(String text) {
            }
        });
        assertEquals(FrameCodec.CODEC_ZSTD, codec.compressionCodec(), "zstd initialised: " + warnings);
        byte[] encoded = codec.encode(message);
        assertEquals(FrameCodec.CODEC_ZSTD, encoded[0]);
        assertArrayEquals(message, codec.decode(encoded));
        assertTrue(warnings.isEmpty(), warnings.toString());
    }

    @Test
    void theJarIsRelocatedAndCarriesItsDescriptor() throws Exception {
        try (JarFile jar = new JarFile(System.getProperty("pandaeye.jar"))) {
            assertTrue(jar.stream().noneMatch(e -> e.getName().startsWith("com/google/") || e.getName().startsWith("io/")),
                    "every library is relocated under dev/pandascript/pandaeye/libs");
            String descriptor = new String(jar.getInputStream(jar.getEntry("plugin.yml")).readAllBytes(),
                    StandardCharsets.UTF_8);
            assertTrue(descriptor.contains("main: dev.pandascript.pandaeye.paper.PandaEyePlugin"));
            assertTrue(descriptor.contains("folia-supported: true"));
            assertTrue(!descriptor.contains("${version}"), "version expanded");
            assertTrue(jar.getEntry("dev/pandascript/pandaeye/paper/PandaEyePlugin.class") != null);
            assertTrue(jar.getEntry("config.yml") != null);
        }
    }
}
