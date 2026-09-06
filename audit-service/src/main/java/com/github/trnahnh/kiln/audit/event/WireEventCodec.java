package com.github.trnahnh.kiln.audit.event;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.UUID;

import com.github.trnahnh.kiln.audit.chain.CanonicalJson;
import com.github.trnahnh.kiln.audit.chain.HashChain;

import tools.jackson.databind.JsonNode;
import tools.jackson.databind.node.JsonNodeType;
import tools.jackson.databind.node.ObjectNode;

public final class WireEventCodec {

    private WireEventCodec() {
    }

    public static WireEvent decode(byte[] value) {
        JsonNode root;
        try {
            root = CanonicalJson.mapper().readTree(value);
        } catch (RuntimeException e) {
            throw new MalformedEventException("record is not JSON", e);
        }
        if (root == null || root.getNodeType() != JsonNodeType.OBJECT) {
            throw new MalformedEventException("record is not a JSON object");
        }
        UUID eventId;
        try {
            eventId = UUID.fromString(required(root, "eventId"));
        } catch (IllegalArgumentException e) {
            throw new MalformedEventException("eventId is not a uuid", e);
        }
        Instant timestamp;
        try {
            timestamp = Instant.parse(required(root, "timestamp"));
        } catch (RuntimeException e) {
            throw new MalformedEventException("timestamp is not RFC 3339", e);
        }
        JsonNode details = root.get("details");
        String canonical;
        if (details == null || details.isNull()) {
            canonical = "{}";
        } else if (details.getNodeType() != JsonNodeType.OBJECT) {
            throw new MalformedEventException("details is not an object");
        } else {
            canonical = CanonicalJson.canonicalize(details);
        }
        return new WireEvent(eventId, required(root, "actor"), required(root, "action"), required(root, "resource"),
                HashChain.truncate(timestamp), canonical);
    }

    public static byte[] encode(WireEvent event) {
        ObjectNode node = CanonicalJson.mapper().createObjectNode();
        node.put("eventId", event.eventId().toString());
        node.put("actor", event.actor());
        node.put("action", event.action());
        node.put("resource", event.resource());
        node.put("timestamp", HashChain.formatOccurredAt(event.timestamp()));
        node.set("details", CanonicalJson.mapper().readTree(event.details()));
        return CanonicalJson.mapper().writeValueAsString(node).getBytes(StandardCharsets.UTF_8);
    }

    private static String required(JsonNode root, String field) {
        JsonNode node = root.get(field);
        if (node == null || node.isNull() || node.getNodeType() != JsonNodeType.STRING || node.stringValue().isEmpty()) {
            throw new MalformedEventException(field + " is missing or not a non-empty string");
        }
        return node.stringValue();
    }
}
