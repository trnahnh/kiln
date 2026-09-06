# ADR-0017: Audit publishing is asynchronous and never blocks a reconcile

**Status:** Accepted, 2026-09-06.

## Context

Phase 6 makes every subsystem publish its actions to Kafka so the Audit/RBAC service can chain them. A controller that has just moved a `TenantDatabase` to `Ready`, promoted a canary, or aborted a chaos experiment must publish that fact, and Kafka can be down when it does. Two stances are possible: write-ahead, where the controller waits for the broker's acknowledgement before it commits the transition, so no action ever happens unaudited; or asynchronous, where the transition commits regardless and the publish is retried in the background. The first couples every subsystem's liveness to Kafka; the second can lose events in an outage.

## Decision

Publishing is asynchronous. The shared `audit` Go module accepts an event into a bounded in-memory buffer and returns at once; a background producer delivers it with retries for up to two minutes. A reconcile never waits on Kafka and never fails because of it.

A publish that is finally given up is not silent: it increments `kiln_audit_publish_failures_total{action}` on the publishing controller's metrics endpoint and raises a Warning Event `AuditPublishFailed` on the resource. An event that could not be buffered because the buffer is full is counted and reported the same way.

Event ids are deterministic (UUID v5 from the resource, action and the transition's own key), so a reconcile that repeats after a failed status write republishes the same id and the service stores it once.

## Consequences

- A Kafka outage never becomes a provisioning, rollout or chaos-revert outage. In particular a chaos abort, which is a safety action, is never delayed by the audit path.
- An outage longer than the retry window loses events. The loss is visible in a metric and in Events, and Phase 7's validation plan, which requires every simulated action to produce a real event, must assert on that counter being zero rather than assume it.
- The buffer is memory only; a controller that crashes loses what it had not yet delivered. A durable outbox would close that gap and is the natural successor if the loss ever matters in practice.
