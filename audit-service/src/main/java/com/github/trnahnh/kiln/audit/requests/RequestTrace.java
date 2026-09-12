package com.github.trnahnh.kiln.audit.requests;

import java.util.HashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.function.Supplier;

import org.springframework.stereotype.Component;

import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import io.micrometer.tracing.propagation.Propagator;

/**
 * The request's trace as the REST path hands it on: its W3C traceparent is stamped on the
 * CR so every controller's spans join it, and the admission call is a child span so the
 * policy check is visible on its own (ADR-0021).
 */
@Component
public class RequestTrace {

    public static final String ANNOTATION_TRACEPARENT = "platform.internal/traceparent";

    private static final String HEADER_TRACEPARENT = "traceparent";

    private final Tracer tracer;
    private final Propagator propagator;

    public RequestTrace(Tracer tracer, Propagator propagator) {
        this.tracer = tracer;
        this.propagator = propagator;
    }

    /** The W3C traceparent of the request being served, empty when nothing is traced. */
    public Optional<String> current() {
        Span span = tracer.currentSpan();
        if (span == null || span.isNoop()) {
            return Optional.empty();
        }
        Map<String, String> carrier = new HashMap<>();
        propagator.inject(span.context(), carrier, (c, key, value) -> carrier.put(key, value));
        return Optional.ofNullable(carrier.get(HEADER_TRACEPARENT));
    }

    /** Runs the admission step as a child span named after what was submitted. */
    public <T> T admission(String resource, Supplier<T> apply) {
        Span span = tracer.nextSpan().name("admission").tag("kiln.resource", Objects.requireNonNull(resource)).start();
        try (Tracer.SpanInScope ignored = tracer.withSpan(span)) {
            return apply.get();
        } catch (RuntimeException e) {
            span.error(e);
            throw e;
        } finally {
            span.end();
        }
    }
}
