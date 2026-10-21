package dev.pandascript.pandaeye.common.util;

import dev.pandascript.pandaeye.common.TestLog;
import dev.pandascript.pandaeye.common.time.EventClock;
import org.junit.jupiter.api.Test;

import java.util.UUID;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class UtilTest {

    @Test
    void uuidBytesAreBigEndian() {
        UUID uuid = UUID.fromString("01234567-89ab-cdef-0123-456789abcdef");
        byte[] bytes = Uuids.toBytes(uuid);
        assertEquals(0x01, bytes[0]);
        assertEquals((byte) 0xef, bytes[15]);
        assertEquals(uuid, Uuids.fromBytes(bytes, 0));
        assertEquals(uuid, Uuids.fromByteString(Uuids.toByteString(uuid)).orElseThrow());
        assertTrue(Uuids.fromByteString(com.google.protobuf.ByteString.copyFrom(new byte[3])).isEmpty());
    }

    @Test
    void canonicalUuidsAreLowercaseAndDashed() {
        assertTrue(Uuids.isCanonical(UUID.randomUUID().toString()));
        assertFalse(Uuids.isCanonical("01234567-89AB-cdef-0123-456789abcdef"));
        assertFalse(Uuids.isCanonical("0123456789abcdef0123456789abcdef"));
        assertFalse(Uuids.isCanonical("*"));
        assertFalse(Uuids.isCanonical(null));
    }

    @Test
    void eventClockIsMonotonicMicros() {
        AtomicLong nanos = new AtomicLong(5_000_000_000L);
        EventClock clock = new EventClock(1_000_000L, 5_000_000_000L, nanos::get);
        assertEquals(1_000_000L, clock.nowMicros());
        nanos.addAndGet(2_500_000);
        assertEquals(1_002_500L, clock.nowMicros());
        assertEquals(1_002L, clock.nowMillis());
        assertEquals(5_002_000_000L, clock.unixMillisToNanos(1_002));
    }

    @Test
    void tokenBucketRefillsAndAllowsOneOversizedRequestWhenFull() {
        TokenBucket bucket = new TokenBucket(10, 10, 0);
        assertTrue(bucket.tryAcquire(0, 10));
        assertFalse(bucket.tryAcquire(0, 1));
        assertTrue(bucket.tryAcquire(100_000_000L, 1));
        assertTrue(bucket.tryAcquire(2_000_000_000L, 50), "oversized request admitted when full");
        assertFalse(bucket.tryAcquire(3_000_000_000L, 1), "and leaves the bucket in debt");
    }

    @Test
    void rateLimitedLogLogsOncePerPeriodPerSite() {
        TestLog log = new TestLog();
        RateLimitedLog limited = new RateLimitedLog(log, 10_000_000_000L);
        for (int i = 0; i < 5; i++) {
            limited.warn("receive", "boom", new IllegalStateException("x"));
        }
        limited.warn("send", "boom", null);
        assertEquals(2, log.lines().size());
        assertEquals(6, limited.total());
    }

    @Test
    void fingerprintsNeverContainTheSecret() {
        String fp = Fingerprint.of("SUPER-SECRET-LICENSE");
        assertTrue(fp.startsWith("sha256:"));
        assertEquals(15, fp.length());
        assertFalse(fp.contains("SECRET"));
        assertEquals("<empty>", Fingerprint.of(""));
    }
}
