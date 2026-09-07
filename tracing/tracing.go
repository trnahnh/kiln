// Package tracing is how every subsystem joins the one trace a request belongs to: the
// audit service opens it on POST /v1/requests and stamps it on the CR it applies, every
// controller parents its spans on that annotation, and every audit record carries it in
// its Kafka headers (ADR-0021).
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// AnnotationTraceParent carries a W3C traceparent on a CR, stamped by the audit service's
// REST path and copied to composed resources and owned pod templates.
const AnnotationTraceParent = "platform.internal/traceparent"

const headerTraceParent = "traceparent"

func init() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
}

// Setup exports spans to an OTLP gRPC endpoint. An empty endpoint leaves the no-op global
// provider in place, so a process with nothing listening runs unchanged; the returned
// function flushes and stops the exporter.
func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("otlp exporter for %s: %w", endpoint, err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(service)))
	if err != nil {
		return nil, fmt.Errorf("trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// Parent returns ctx carrying the trace annotated on an object; without the annotation
// ctx is returned unchanged.
func Parent(ctx context.Context, annotations map[string]string) context.Context {
	tp, ok := annotations[AnnotationTraceParent]
	if !ok || tp == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{headerTraceParent: tp})
}

// Stamp writes the span in ctx onto annotations so a later reconcile of the object can
// parent on it. Nothing is written when ctx carries no valid span.
func Stamp(ctx context.Context, annotations map[string]string) map[string]string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return annotations
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationTraceParent] = carrier[headerTraceParent]
	return annotations
}

// Inherit copies the traceparent from one object's annotations to another's, for owned
// objects such as a pod template whose scheduling belongs to the same request.
func Inherit(from, to map[string]string) map[string]string {
	tp, ok := from[AnnotationTraceParent]
	if !ok || tp == "" {
		return to
	}
	if to == nil {
		to = map[string]string{}
	}
	to[AnnotationTraceParent] = tp
	return to
}

// Span opens one span of the trace annotated on an object, or of a new trace when the
// object carries none. began backdates the start so the span's duration is the transition's.
func Span(ctx context.Context, tracer string, annotations map[string]string, name string, began time.Time, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx = Parent(ctx, annotations)
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	if !began.IsZero() {
		opts = append(opts, trace.WithTimestamp(began))
	}
	return otel.Tracer(tracer).Start(ctx, name, opts...)
}

// Headers renders the span in ctx as W3C propagation headers for a message; nil when ctx
// carries no valid span.
func Headers(ctx context.Context) map[string]string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

// FromHeaders returns ctx carrying the trace in a message's propagation headers.
func FromHeaders(ctx context.Context, headers map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(headers))
}

// TraceID of the span in ctx, empty when there is none.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
