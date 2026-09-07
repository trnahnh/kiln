package com.github.trnahnh.kiln.audit.requests;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import org.junit.jupiter.api.Test;

import io.micrometer.tracing.Tracer;
import io.micrometer.tracing.propagation.Propagator;
import io.micrometer.tracing.test.simple.SimpleTracer;

class RequestTraceTest {

    @Test
    void nothingTracedMeansNothingStampedAndAdmissionStillRuns() {
        RequestTrace trace = new RequestTrace(Tracer.NOOP, Propagator.NOOP);
        assertThat(trace.current()).isEmpty();
        assertThat(trace.admission("DatabaseClaim/ns/x", () -> "applied")).isEqualTo("applied");
    }

    @Test
    void admissionIsAChildSpanThatRecordsTheRejection() {
        SimpleTracer tracer = new SimpleTracer();
        RequestTrace trace = new RequestTrace(tracer, Propagator.NOOP);
        AdmissionRejectedException rejected = new AdmissionRejectedException(400, "POLICY_DENIED rule=storage-ceiling", null);

        assertThatThrownBy(() -> trace.admission("DatabaseClaim/ns/x", () -> {
            throw rejected;
        })).isSameAs(rejected);

        assertThat(tracer.getSpans()).hasSize(1);
        var span = tracer.getSpans().getFirst();
        assertThat(span.getName()).isEqualTo("admission");
        assertThat(span.getTags()).containsEntry("kiln.resource", "DatabaseClaim/ns/x");
        assertThat(span.getError()).isSameAs(rejected);
        assertThat(span.getEndTimestamp()).isNotNull();
    }
}
