package tracing

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func recording(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })
	return rec
}

func TestStampThenSpanContinuesTheSameTrace(t *testing.T) {
	rec := recording(t)
	ctx, root := otel.Tracer("test").Start(context.Background(), "POST /v1/requests")
	annotations := Stamp(ctx, map[string]string{"platform.internal/requested-by": "dev@x"})
	root.End()
	if annotations[AnnotationTraceParent] == "" {
		t.Fatal("Stamp must write the traceparent annotation")
	}
	if annotations["platform.internal/requested-by"] != "dev@x" {
		t.Fatal("Stamp must keep the other annotations")
	}

	began := time.Now().Add(-90 * time.Second)
	childCtx, child := Span(context.Background(), "operator", annotations, "PROVISION", began, attribute.String("outcome", "Ready"))
	child.End()

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
	if spans[1].SpanContext().TraceID() != spans[0].SpanContext().TraceID() {
		t.Fatal("the controller span must belong to the request's trace")
	}
	if spans[1].Parent().SpanID() != spans[0].SpanContext().SpanID() {
		t.Fatal("the controller span must be a child of the request span")
	}
	if !spans[1].StartTime().Equal(began) {
		t.Fatalf("the span must be backdated to when the transition began, got %v", spans[1].StartTime())
	}
	if TraceID(childCtx) != spans[0].SpanContext().TraceID().String() {
		t.Fatal("TraceID must read the trace of the span in ctx")
	}
}

func TestSpanWithoutAnnotationStartsANewTrace(t *testing.T) {
	rec := recording(t)
	_, span := Span(context.Background(), "scheduler", nil, "SCHEDULE", time.Time{})
	span.End()
	if got := rec.Ended(); len(got) != 1 || got[0].Parent().IsValid() {
		t.Fatal("a pod without a traceparent yields a root span")
	}
}

func TestHeadersRoundTrip(t *testing.T) {
	recording(t)
	ctx, span := otel.Tracer("test").Start(context.Background(), "publish")
	defer span.End()
	headers := Headers(ctx)
	if headers["traceparent"] == "" {
		t.Fatalf("want a traceparent header, got %v", headers)
	}
	if got := TraceID(FromHeaders(context.Background(), headers)); got != TraceID(ctx) {
		t.Fatalf("headers must carry the trace: %s != %s", got, TraceID(ctx))
	}
	if Headers(context.Background()) != nil {
		t.Fatal("no span, no headers")
	}
	if Stamp(context.Background(), nil) != nil {
		t.Fatal("no span, no annotation")
	}
}

func TestInherit(t *testing.T) {
	to := Inherit(map[string]string{AnnotationTraceParent: "00-abc-def-01"}, nil)
	if to[AnnotationTraceParent] != "00-abc-def-01" {
		t.Fatal(to)
	}
	if Inherit(nil, nil) != nil {
		t.Fatal("nothing to inherit must add nothing")
	}
}

func TestSetupWithoutEndpointIsANoop(t *testing.T) {
	shutdown, err := Setup(context.Background(), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("no endpoint must leave the global provider untouched")
	}
}

func TestSetupWithAnEndpointInstallsAnExportingProvider(t *testing.T) {
	shutdown, err := Setup(context.Background(), "kiln-test", "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer otel.SetTracerProvider(noop.NewTracerProvider())
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("an endpoint must install the SDK provider, got %T", otel.GetTracerProvider())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(ctx)
}
