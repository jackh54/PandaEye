package dev.pandascript.pandaeye.common.transport;

import com.google.protobuf.CodedOutputStream;
import com.google.protobuf.WireFormat;
import dev.pandascript.pandaeye.protocol.v1.Batch;
import dev.pandascript.pandaeye.protocol.v1.ClientFrame;

import java.io.IOException;
import java.io.UncheckedIOException;

/** Serialises ClientFrames (before the codec byte is added). */
final class Frames {

    private Frames() {
    }

    /**
     * A {@code ClientFrame{batch}}: the batch header (seq, sent time, replay bit) followed by the
     * pre-serialised events. Concatenated protobuf encodings of one message type merge, so this equals the
     * encoding of the complete Batch.
     */
    static byte[] batch(PendingBatch batch, long sentUnixMs) {
        Batch header = Batch.newBuilder()
                .setSeq(batch.seq)
                .setSentUnixMs(sentUnixMs)
                .setReplayed(batch.replay)
                .build();
        int batchSize = header.getSerializedSize() + batch.events.length;
        int total = CodedOutputStream.computeTagSize(ClientFrame.BATCH_FIELD_NUMBER)
                + CodedOutputStream.computeUInt32SizeNoTag(batchSize) + batchSize;
        byte[] out = new byte[total];
        CodedOutputStream stream = CodedOutputStream.newInstance(out);
        try {
            stream.writeTag(ClientFrame.BATCH_FIELD_NUMBER, WireFormat.WIRETYPE_LENGTH_DELIMITED);
            stream.writeUInt32NoTag(batchSize);
            header.writeTo(stream);
            stream.writeRawBytes(batch.events);
            stream.checkNoSpaceLeft();
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        return out;
    }
}
