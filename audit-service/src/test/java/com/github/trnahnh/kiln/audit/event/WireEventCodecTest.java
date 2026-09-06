package com.github.trnahnh.kiln.audit.event;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.UUID;

import org.junit.jupiter.api.Test;

class WireEventCodecTest {

    private static byte[] bytes(String s) {
        return s.getBytes(StandardCharsets.UTF_8);
    }

    @Test
    void decodesTheDocumentedShapeAndCanonicalisesDetails() {
        WireEvent e = WireEventCodec.decode(bytes("""
                {"eventId":"6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10","actor":"user@company.com","action":"PROVISION",
                 "resource":"TenantDatabase/team-checkout/checkout-db","timestamp":"2026-09-04T18:00:00.123456789Z",
                 "details":{"z":1,"outcome":"Ready"}}
                """));
        assertThat(e.eventId()).isEqualTo(UUID.fromString("6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10"));
        assertThat(e.timestamp()).isEqualTo(Instant.parse("2026-09-04T18:00:00.123456Z"));
        assertThat(e.details()).isEqualTo("{\"outcome\":\"Ready\",\"z\":1}");
    }

    @Test
    void absentOrNullDetailsBecomeEmptyObject() {
        String base = "\"eventId\":\"6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10\",\"actor\":\"a\",\"action\":\"b\",\"resource\":\"c\",\"timestamp\":\"2026-09-04T18:00:00Z\"";
        assertThat(WireEventCodec.decode(bytes("{" + base + "}")).details()).isEqualTo("{}");
        assertThat(WireEventCodec.decode(bytes("{" + base + ",\"details\":null}")).details()).isEqualTo("{}");
    }

    @Test
    void rejectsMalformedRecords() {
        String base = "\"actor\":\"a\",\"action\":\"b\",\"resource\":\"c\",\"timestamp\":\"2026-09-04T18:00:00Z\"";
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("nope"))).isInstanceOf(MalformedEventException.class);
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("[]"))).isInstanceOf(MalformedEventException.class);
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("{\"eventId\":\"x\"," + base + "}"))).hasMessageContaining("eventId");
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("{" + base + "}"))).hasMessageContaining("eventId");
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("{\"eventId\":\"6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10\",\"actor\":\"a\",\"action\":\"b\",\"resource\":\"c\",\"timestamp\":\"yesterday\"}")))
                .hasMessageContaining("timestamp");
        assertThatThrownBy(() -> WireEventCodec.decode(bytes("{\"eventId\":\"6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10\"," + base + ",\"details\":[1]}")))
                .hasMessageContaining("details");
    }

    @Test
    void encodeRoundTrips() {
        WireEvent e = new WireEvent(UUID.randomUUID(), "u", "DEPLOY", "CanaryRollout/ns/x",
                Instant.parse("2026-09-04T18:00:00.5Z"), "{\"outcome\":\"Started\",\"templateHash\":\"abc\"}");
        String json = new String(WireEventCodec.encode(e), StandardCharsets.UTF_8);
        assertThat(json).contains("\"timestamp\":\"2026-09-04T18:00:00.500000Z\"").doesNotContain("prevHash");
        assertThat(WireEventCodec.decode(WireEventCodec.encode(e))).isEqualTo(e);
    }
}
