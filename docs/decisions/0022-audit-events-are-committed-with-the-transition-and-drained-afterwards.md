# ADR-0022: Audit events are committed with the transition and drained afterwards

**Status:** Accepted, 2026-09-12. Supersedes the memory-only buffer of [ADR-0017](0017-audit-publishing-never-blocks-a-reconcile.md); its stance that a reconcile never waits on Kafka stands.

## Context

ADR-0017 chose asynchronous publishing and named its residual: the buffer is memory only, so a controller that crashes loses what it had not yet delivered. As built, the exposure is wider than that sentence. Every controller publishes *before* it patches the transition into the CR's status, from a 1024-slot channel that drops an event outright when full, and the only thing that reconciles the two is the deterministic event id. A crash between the publish call and the broker's acknowledgement loses the event; a crash between the publish and the status patch republishes it on the next reconcile, which the unique constraint absorbs. Phase 7's simulated week asserted `kiln_audit_publish_failures_total` at zero, but that run killed no controller.

The REST path has the mirror-image exposure: `POST /v1/requests` applies the CR and then publishes `PROVISION_REQUEST` synchronously, so a crash between the two leaves an object with no request row, and `POLICY_DENY`'s id is derived from the wall clock, so a client that retries after a timeout would write a second row for one denial.

The platform's claim is that every action is on the trail. That has to hold under crashes, not only under a healthy run.

## Decision

- **The transition and the intent to audit are one write.** A controller that moves a CR through an audited transition records the wire event in `status.audit.pending` in the same status patch that commits the transition. Either both reach etcd or neither does. The event's `timestamp` is set at that write, so `occurred_at` on the row is when the transition happened, not when it was delivered.
- **A drainer publishes and clears.** Each controller runs one drain goroutine, keyed by CR, signalled from the reconcile that added the event. It produces with all-in-sync-replica acknowledgement, waits for it, then patches the delivered event out of `status.audit.pending`. A reconcile only enqueues; it never waits on Kafka, so ADR-0017's guarantee that a Kafka outage cannot become a provisioning, rollout or chaos-revert outage is unchanged. A chaos abort in particular commits and removes its fault before any audit I/O.
- **Dedup is unchanged.** A crash after the acknowledgement and before the clearing patch republishes the event on restart. Its id is deterministic, so the audit service stores it once by the unique constraint, and no publisher keeps its own record of what it sent. The invariant that the hash chain and the `eventId` constraint are the only tamper-evidence and dedup mechanisms stands.
- **The backlog is unbounded and visible.** Nothing is dropped. While Kafka is unreachable the pending list grows in the CR's status; the gauge `kiln_audit_outbox_pending` reports it per controller, and a CR whose pending list passes a threshold raises the Warning Event `AuditOutboxBacklog`. `kiln_audit_publish_failures_total` now counts only events that fail validation, which is a bug, not an outage.
- **The REST path publishes before it applies.** `POST /v1/requests` publishes `PROVISION_REQUEST` and waits for the acknowledgement, then applies; an admission rejection publishes `POLICY_DENY` the same way. Both ids are derived from the request's trace id, or a fresh id per request when nothing is traced, so a retry of the same request carries the same ids and the constraint absorbs it. The residual is a crash between the acknowledgement and the apply, which leaves a `Received` row with no object: visible on the trail, never a lost action.
- **The scheduler's `SCHEDULE` event is the named exception.** It is published from PostBind, where the binding is already durable in the API server and there is no platform CR to carry an outbox. It keeps the memory buffer of ADR-0017, and the residual is stated here rather than closed.

## Consequences

- A `TenantDatabase`, `CanaryRollout` or `ChaosExperiment` may show pending audit events in its status; that list is empty on a healthy cluster within one round trip to Kafka. The status schema change reaches the local cluster only through Git, as every CRD change does.
- A controller restart resumes delivery from the CRs' own status, so the proof of this decision is a crash test: with every controller SIGKILLed repeatedly during a burst, every transition an independent informer observed has exactly one row (Phase 8's exit criterion in [`ROADMAP.md`](../ROADMAP.md)).
- The `audit` module's `OnFailure` and buffer-full paths remain for the scheduler and for validation failures; the controllers no longer take them.
- The drainer clears an event with a JSON patch that tests the element is still the one it delivered, so a reconcile that appended in the meantime is never overwritten. The other direction is tolerated rather than prevented: a reconcile working from a cache that predates a clear re-adds the delivered event in its merge patch, the drainer delivers it again, and the constraint stores nothing. One extra record on the topic, never a missing or a duplicated row.
- A durable outbox makes the CR status carry audit payloads. The `details` of an event are small by construction, but a transition that produced a large `details` object would now sit in etcd until delivered; none does today, and the threshold Event is what would show it.
- A `POLICY_DENY` of a direct `kubectl apply` still creates no object and no event ([ADR-0018](0018-the-audit-service-alone-computes-the-hash-chain.md)); this decision changes nothing there.
