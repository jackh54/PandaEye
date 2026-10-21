package dev.pandascript.pandaeye.common.transport;

import java.net.http.WebSocket;
import java.nio.ByteBuffer;
import java.util.Arrays;
import java.util.concurrent.CompletionStage;

/**
 * WebSocket listener for one connection attempt. Message parts are accumulated (up to the frame limit) and
 * whole messages are handed to the cloud thread; {@code request(1)} follows every callback. The JDK
 * serialises callbacks per WebSocket, so the accumulation buffer needs no locking.
 */
final class Connection implements WebSocket.Listener {

    private final CloudClient client;
    private final int generation;
    private final int maxMessageBytes;
    private byte[] partial = new byte[0];
    private int length;
    private boolean overflow;

    Connection(CloudClient client, int generation, int maxMessageBytes) {
        this.client = client;
        this.generation = generation;
        this.maxMessageBytes = maxMessageBytes;
    }

    @Override
    public void onOpen(WebSocket webSocket) {
        webSocket.request(1);
    }

    @Override
    public CompletionStage<?> onBinary(WebSocket webSocket, ByteBuffer data, boolean last) {
        int size = data.remaining();
        if (!overflow && length + size <= maxMessageBytes) {
            if (length + size > partial.length) {
                partial = Arrays.copyOf(partial, Math.min(maxMessageBytes, Math.max(length + size, partial.length * 2)));
            }
            data.get(partial, length, size);
            length += size;
        } else {
            overflow = true;
        }
        if (last) {
            if (overflow) {
                client.onProtocolViolation(generation, "message from the cloud exceeds " + maxMessageBytes + " bytes");
            } else {
                client.onMessage(generation, Arrays.copyOf(partial, length));
            }
            partial = new byte[0];
            length = 0;
            overflow = false;
        }
        webSocket.request(1);
        return null;
    }

    @Override
    public CompletionStage<?> onText(WebSocket webSocket, CharSequence data, boolean last) {
        webSocket.request(1);
        return null;
    }

    @Override
    public CompletionStage<?> onClose(WebSocket webSocket, int statusCode, String reason) {
        client.onClosed(generation, statusCode, reason);
        return null;
    }

    @Override
    public void onError(WebSocket webSocket, Throwable error) {
        client.onTransportError(generation, error);
    }
}
