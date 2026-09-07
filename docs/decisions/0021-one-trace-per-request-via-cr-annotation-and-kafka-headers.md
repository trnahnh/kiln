# ADR-0021: One trace per request, carried by a CR annotation and Kafka headers

**Status:** Accepted, 2026-09-06.

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#cross-cutting-observability) promises that a single request can be followed from submission through policy check, provisioning, scheduling and deployment in one OpenTelemetry trace. Nothing had been built for it before Phase 7. A request through `POST /v1/requests` crosses three asynchronous boundaries that ordinary HTTP context propagation does not cover: the service answers `202` long before any controller acts on the CR it applied; every controller's work is a reconcile triggered by a watch, with no caller; and every audit event travels through Kafka to a consumer in another process.

Three things were open: where spans are stored, how the trace crosses those boundaries, and how much of a controller's work becomes a span.

## Decision

- **Jaeger v2 all-in-one, as plain manifests** in `gitops/observability/`, in-memory storage, OTLP in, query API out, added to Grafana as a datasource. This stays inside [ADR-0001](0001-observability-as-plain-manifests.md): flat manifests, no Helm, no persistence. Every subsystem exports over OTLP gRPC; the endpoint is a flag (`--otlp-endpoint` on the Go controllers, `tracing.endpoint` in the scheduler's plugin arguments, the OTLP endpoint property on the audit service) and an **unset endpoint means a no-op tracer**, so envtest, the operator's integration job and any cluster without Jaeger run unchanged.
- **The trace crosses the CR boundary as an annotation.** The REST path is the root span. It stamps `platform.internal/traceparent`, the W3C `traceparent` value of that span, on every CR it applies, next to `platform.internal/requested-by`. The standard composition copies it from the `DatabaseClaim` to the `TenantDatabase` exactly as it copies the actor. The operator copies it onto its StatefulSet's pod template. Every controller parents its spans on the annotation; a CR or pod without one starts a trace of its own. Crossplane's compose step has no span, because Crossplane is third-party and uninstrumented; its latency is the visible gap between the request span and the operator's first span.
- **The trace crosses Kafka as record headers.** Every audit record carries W3C `traceparent` and `tracestate` headers with the publishing span. The audit service consumes with Spring Kafka's observation enabled, so its consume-and-store span is a child of the producer's. The trace id is never put in the event body: `details` is hashed content and the wire schema stays as [ADR-0018](0018-the-audit-service-alone-computes-the-hash-chain.md) defines it.
- **One span per audited action.** A controller opens a span when it publishes a transition, named by the audit action, backdated to when the transition began (CR creation for `PROVISION`, the rollout's start for `DEPLOY` and `ROLLBACK`, the fault going live for a chaos result, the Job's creation for a backup or restore result) and closed when the event is published. Spans and audit events line up one to one; periodic reconciles that change nothing produce no span. The scheduler's `SCHEDULE` span runs from the pod's creation to its binding.
- **Database pods go through `kiln-scheduler`** when the operator is given `--scheduler-name`, which the in-cluster deploy sets and nothing else does, and they carry the `latency-sensitive` workload class so a database is never placed on spot capacity ([ADR-0009](0009-workload-class-label-gates-spot-placement.md)). This is what puts a `SCHEDULE` span inside a provisioning request's trace.
- The shared Go module is `tracing`, one owner for the setup, the annotation carrier and the header carrier; `audit.Publisher.Publish` takes a context so the publisher can capture the span at the call.

## Consequences

- A provisioning request is one trace: the HTTP span, the `admission` child around the server-side apply, the producer and consumer spans of `PROVISION_REQUEST`, the operator's `PROVISION` span, the scheduler's `SCHEDULE` span for the database pod, and the producer and consumer spans of each of those events, all under one trace id. A rollout request and an experiment request are traces of the same shape with `DEPLOY`/`ROLLBACK` and `CHAOS_EXPERIMENT` spans.
- Every rollout of a service belongs to the trace of the request that enabled canary delivery for it, because the trigger is a pod-template change with no request of its own. A deploy that should be its own trace would need a second propagation rule; that is not built.
- A pod the identities' own Deployments create carries no traceparent, so its `SCHEDULE` span is a root. Only the operator's pods are guaranteed to join a request's trace.
- Traces are lost when the Jaeger pod restarts and the oldest are evicted after 100,000; acceptable for the dev and CI cluster, as ADR-0001 accepts for metrics.
- Anything that needs a Collector, sampling below 100%, or a persistent trace store supersedes this ADR.
