package com.github.trnahnh.kiln.audit.chain;

import static org.assertj.core.api.Assertions.assertThat;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.UUID;

import org.junit.jupiter.api.Test;

class HashChainTest {

    private static final UUID ID = UUID.fromString("6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10");
    private static final Instant AT = Instant.parse("2026-09-04T18:00:00.123456789Z");

    @Test
    void knownAnswerMatchesTheDocumentedFormula() throws Exception {
        String hash = HashChain.hash(HashChain.GENESIS, ID, "user@company.com", "PROVISION",
                "TenantDatabase/team-checkout/checkout-db", AT, "{\"z\": 1, \"outcome\": \"Ready\"}");

        String content = String.join("\n",
                "0".repeat(64),
                "6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10",
                "user@company.com",
                "PROVISION",
                "TenantDatabase/team-checkout/checkout-db",
                "2026-09-04T18:00:00.123456Z",
                "{\"outcome\":\"Ready\",\"z\":1}");
        String expected = HexFormat.of().formatHex(
                MessageDigest.getInstance("SHA-256").digest(content.getBytes(StandardCharsets.UTF_8)));
        assertThat(hash).isEqualTo(expected).hasSize(64).matches("[0-9a-f]+");
    }

    @Test
    void timestampIsTruncatedToMicrosecondsInUtc() {
        assertThat(HashChain.formatOccurredAt(Instant.parse("2026-09-04T18:00:00Z"))).isEqualTo("2026-09-04T18:00:00.000000Z");
        assertThat(HashChain.formatOccurredAt(AT)).isEqualTo("2026-09-04T18:00:00.123456Z");
        assertThat(HashChain.hash(HashChain.GENESIS, ID, "a", "b", "c", AT, "{}"))
                .isEqualTo(HashChain.hash(HashChain.GENESIS, ID, "a", "b", "c", HashChain.truncate(AT), "{}"));
    }

    @Test
    void detailsByteLayoutDoesNotChangeTheHash() {
        String a = HashChain.hash(HashChain.GENESIS, ID, "a", "b", "c", AT, "{\"x\":1,\"y\":{\"n\":2,\"m\":3}}");
        String b = HashChain.hash(HashChain.GENESIS, ID, "a", "b", "c", AT, "{ \"y\" : { \"m\" : 3 , \"n\" : 2 } , \"x\" : 1 }");
        assertThat(a).isEqualTo(b);
    }

    @Test
    void chainOfThreeVerifiesAndEveryTamperIsNamed() {
        List<HashChain.Link> chain = chain(3);
        assertThat(HashChain.verify(chain)).isEmpty();

        List<HashChain.Link> content = new ArrayList<>(chain);
        HashChain.Link l = content.get(1);
        content.set(1, new HashChain.Link(l.seq(), l.eventId(), "mallory", l.action(), l.resource(), l.occurredAt(),
                l.details(), l.prevHash(), l.hash()));
        assertThat(HashChain.verify(content)).containsExactly(new HashChain.BrokenLink(2, l.eventId(), "hash mismatch"));

        List<HashChain.Link> prev = new ArrayList<>(chain);
        HashChain.Link p = prev.get(2);
        prev.set(2, new HashChain.Link(p.seq(), p.eventId(), p.actor(), p.action(), p.resource(), p.occurredAt(),
                p.details(), "f".repeat(64), p.hash()));
        assertThat(HashChain.verify(prev)).containsExactly(
                new HashChain.BrokenLink(3, p.eventId(), "prevHash mismatch"),
                new HashChain.BrokenLink(3, p.eventId(), "hash mismatch"));

        List<HashChain.Link> rehashed = new ArrayList<>(chain);
        HashChain.Link r = rehashed.get(0);
        HashChain.Link forged = new HashChain.Link(r.seq(), r.eventId(), "mallory", r.action(), r.resource(),
                r.occurredAt(), r.details(), r.prevHash(), null);
        forged = new HashChain.Link(forged.seq(), forged.eventId(), forged.actor(), forged.action(), forged.resource(),
                forged.occurredAt(), forged.details(), forged.prevHash(), HashChain.hash(forged));
        rehashed.set(0, forged);
        assertThat(HashChain.verify(rehashed))
                .as("a re-hashed forgery breaks the next link's prevHash")
                .containsExactly(new HashChain.BrokenLink(2, chain.get(1).eventId(), "prevHash mismatch"));
    }

    @Test
    void emptyChainVerifies() {
        assertThat(HashChain.verify(List.of())).isEmpty();
    }

    static List<HashChain.Link> chain(int n) {
        List<HashChain.Link> out = new ArrayList<>();
        String prev = HashChain.GENESIS;
        for (int i = 1; i <= n; i++) {
            UUID id = UUID.nameUUIDFromBytes(("e" + i).getBytes(StandardCharsets.UTF_8));
            Instant at = AT.plusSeconds(i);
            String details = "{\"outcome\":\"Ready\",\"i\":" + i + "}";
            String hash = HashChain.hash(prev, id, "user", "PROVISION", "TenantDatabase/ns/db" + i, at, details);
            out.add(new HashChain.Link(i, id, "user", "PROVISION", "TenantDatabase/ns/db" + i, at, details, prev, hash));
            prev = hash;
        }
        return out;
    }
}
