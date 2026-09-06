package com.github.trnahnh.kiln.audit.chain;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.List;
import java.util.UUID;

/** The tamper-evidence chain of DATA_MODEL.md, with no Spring or database in sight. */
public final class HashChain {

    public static final String GENESIS = "0".repeat(64);

    private static final DateTimeFormatter OCCURRED_AT =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss.SSSSSS'Z'").withZone(ZoneOffset.UTC);

    private HashChain() {
    }

    public record Link(long seq, UUID eventId, String actor, String action, String resource,
                       Instant occurredAt, String details, String prevHash, String hash) {
    }

    public record BrokenLink(long seq, UUID eventId, String reason) {
    }

    public static Instant truncate(Instant instant) {
        return instant.truncatedTo(ChronoUnit.MICROS);
    }

    public static String formatOccurredAt(Instant instant) {
        return OCCURRED_AT.format(truncate(instant));
    }

    public static String hash(String prevHash, UUID eventId, String actor, String action, String resource,
                              Instant occurredAt, String details) {
        String content = String.join("\n",
                prevHash,
                eventId.toString(),
                actor,
                action,
                resource,
                formatOccurredAt(occurredAt),
                CanonicalJson.canonicalize(details));
        return sha256(content);
    }

    public static String hash(Link link) {
        return hash(link.prevHash(), link.eventId(), link.actor(), link.action(), link.resource(),
                link.occurredAt(), link.details());
    }

    /** Walks links in seq order and names every one whose stored hashes do not hold. */
    public static List<BrokenLink> verify(List<Link> links) {
        List<BrokenLink> broken = new ArrayList<>();
        String expectedPrev = GENESIS;
        for (Link link : links) {
            if (!expectedPrev.equals(link.prevHash())) {
                broken.add(new BrokenLink(link.seq(), link.eventId(), "prevHash mismatch"));
            }
            if (!hash(link).equals(link.hash())) {
                broken.add(new BrokenLink(link.seq(), link.eventId(), "hash mismatch"));
            }
            expectedPrev = link.hash();
        }
        return broken;
    }

    private static String sha256(String content) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            return HexFormat.of().formatHex(digest.digest(content.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 unavailable", e);
        }
    }
}
