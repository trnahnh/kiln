package com.github.trnahnh.kiln.audit.chain;

import java.io.StringWriter;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import tools.jackson.core.JsonGenerator;
import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.JsonNodeType;

/**
 * Canonical JSON per DATA_MODEL.md: keys sorted at every level, no whitespace, numbers as their
 * plain decimal text. The same content always canonicalises to the same bytes whether it
 * arrived on the wire or came back from jsonb, which is what makes the hash content-bound.
 */
public final class CanonicalJson {

    private static final JsonMapper MAPPER = JsonMapper.builder()
            .enable(DeserializationFeature.USE_BIG_DECIMAL_FOR_FLOATS)
            .build();

    private CanonicalJson() {
    }

    public static JsonMapper mapper() {
        return MAPPER;
    }

    public static String canonicalize(String json) {
        JsonNode node;
        try {
            node = MAPPER.readTree(json == null || json.isBlank() ? "{}" : json);
        } catch (RuntimeException e) {
            throw new IllegalArgumentException("details is not JSON: " + json, e);
        }
        if (node.getNodeType() != JsonNodeType.OBJECT) {
            throw new IllegalArgumentException("details must be a JSON object, got " + node.getNodeType());
        }
        return canonicalize(node);
    }

    public static String canonicalize(JsonNode node) {
        StringWriter out = new StringWriter();
        try (JsonGenerator gen = MAPPER.createGenerator(out)) {
            write(node, gen);
        }
        return out.toString();
    }

    private static void write(JsonNode node, JsonGenerator gen) {
        switch (node.getNodeType()) {
            case OBJECT -> {
                gen.writeStartObject();
                List<String> names = new ArrayList<>();
                for (Map.Entry<String, JsonNode> entry : node.properties()) {
                    names.add(entry.getKey());
                }
                names.sort(null);
                for (String name : names) {
                    gen.writeName(name);
                    write(node.get(name), gen);
                }
                gen.writeEndObject();
            }
            case ARRAY -> {
                gen.writeStartArray();
                for (JsonNode child : node) {
                    write(child, gen);
                }
                gen.writeEndArray();
            }
            case NUMBER -> gen.writeRawValue(node.decimalValue().toPlainString());
            case STRING -> gen.writeString(node.stringValue());
            case BOOLEAN -> gen.writeBoolean(node.booleanValue());
            default -> gen.writeNull();
        }
    }
}
