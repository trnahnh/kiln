package com.github.trnahnh.kiln.audit.chain;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import org.junit.jupiter.api.Test;

class CanonicalJsonTest {

    @Test
    void sortsKeysAtEveryLevelAndDropsWhitespace() {
        String canonical = CanonicalJson.canonicalize("{ \"z\": 1, \"a\": { \"y\": [3, {\"q\": false, \"b\": null}], \"x\": \"s\" } }");
        assertThat(canonical).isEqualTo("{\"a\":{\"x\":\"s\",\"y\":[3,{\"b\":null,\"q\":false}]},\"z\":1}");
    }

    @Test
    void numbersKeepTheirStoredTextAndExponentsBecomePlain() {
        assertThat(CanonicalJson.canonicalize("{\"a\":1.10,\"b\":1e2,\"c\":-0.5,\"d\":73.4}"))
                .isEqualTo("{\"a\":1.10,\"b\":100,\"c\":-0.5,\"d\":73.4}");
    }

    @Test
    void unicodeIsPreservedAndEscapesAreStandard() {
        assertThat(CanonicalJson.canonicalize("{\"name\":\"caf\\u00e9 \\\"quoted\\\"\\n\"}"))
                .isEqualTo("{\"name\":\"café \\\"quoted\\\"\\n\"}");
    }

    @Test
    void emptyAndAbsentBecomeEmptyObject() {
        assertThat(CanonicalJson.canonicalize("{}")).isEqualTo("{}");
        assertThat(CanonicalJson.canonicalize("")).isEqualTo("{}");
        assertThat(CanonicalJson.canonicalize((String) null)).isEqualTo("{}");
    }

    @Test
    void rejectsNonObjects() {
        assertThatThrownBy(() -> CanonicalJson.canonicalize("[1,2]")).isInstanceOf(IllegalArgumentException.class);
        assertThatThrownBy(() -> CanonicalJson.canonicalize("not json")).isInstanceOf(IllegalArgumentException.class);
    }

    @Test
    void isIdempotent() {
        String once = CanonicalJson.canonicalize("{\"b\":{\"d\":2,\"c\":1},\"a\":[]}");
        assertThat(CanonicalJson.canonicalize(once)).isEqualTo(once);
    }
}
