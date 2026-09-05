# System Design

## Request flow

1. Developer submits a request (CRD applied via `kubectl`/CLI, or a REST call to the Audit/RBAC service).
2. **Audit/RBAC service** authenticates the caller, checks role permissions, logs the request as `received`.
3. **Policy layer** (OPA/Kyverno admission webhook) validates the request against org rules. Rejected requests are logged as `denied` with a reason and stop here.
4. Approved requests reach the **Provisioning/GitOps layer** (Crossplane + ArgoCD), which translates the high-level request into concrete cloud resources and Kubernetes manifests, applied declaratively.
5. For stateful workloads, the **Operator** takes over lifecycle management (provisioning, backup, restore, scaling) via its own reconciliation loop.
6. The **Scheduler plugin** places any new workload pods with cost/fragmentation-aware scoring instead of default bin packing.
7. Any deployment or update to an existing service goes through the **Progressive delivery controller**, which shifts traffic gradually and auto-rolls-back on metric regression.
8. On demand or on a schedule, the **Chaos/resilience module** injects controlled failure against the service and produces an SLO-based score.
9. Every step above emits an event to the **Audit service**, which hash-chains it into the compliance log.

## 1. Kubernetes Operator (stateful workload lifecycle)

**Purpose:** provision, back up, restore, and scale per-tenant Postgres/Redis instances via a custom CRD.

**Stack:** Go, kubebuilder, controller-runtime, client-go, Helm for packaging.

**Design problems:**

- **Idempotency.** Re-running the reconcile loop on the same object state must produce no side effects. Every reconcile step checks current state before acting, never assumes it's starting from scratch.
- **Concurrency.** A scale-up event arriving while a backup is in progress must be serialized safely, not raced. Solved with a status-driven state machine with explicit phase transitions (`Provisioning`, `Ready`, `Backing Up`, `Restoring`, `Failed`); conflicting operations are rejected, not interleaved.
- **Partial failure.** A PVC created but the StatefulSet creation failing must not leave orphaned cloud resources. Solved with finalizers and owner references so garbage collection is automatic on delete, plus a periodic reconcile sweep that catches drift.

See [`API_REFERENCE.md`](API_REFERENCE.md#tenantdatabase-crd) for the full CRD schema. Decisions that shaped the Phase 1 implementation: [ADR-0002](decisions/0002-one-shot-actions-via-annotations.md) (backup/restore triggered by annotations), [ADR-0003](decisions/0003-scale-is-storage-growth-inside-ready.md) (scaling is storage growth within `Ready`), [ADR-0004](decisions/0004-backups-as-operator-fired-jobs-on-owned-pvc.md) (operator-fired backup Jobs onto an owned volume).

## 2. Provisioning and GitOps layer

**Purpose:** translate a developer's infrastructure request into real cloud resources declaratively, with Git as the source of truth.

**Stack:** Crossplane (Compositions and Composite Resource Definitions), ArgoCD, Terraform (for anything Crossplane doesn't cover), OPA/Kyverno for policy gating before this layer executes.

**Design problems:**

- **Golden-path abstraction boundary.** A developer should be able to request "a standard Postgres database" without specifying instance class, backup retention, or network placement, those are platform defaults. Advanced users need an escape hatch. Solved with tiered Composition Functions: a `standard` tier with sane defaults, and a `custom` tier that exposes the underlying knobs.
- **Drift detection.** If someone manually changes a cloud resource outside of Git, ArgoCD must detect it. Policy differs per resource class: auto-heal for anything stateless, flag-and-alert for anything stateful (never auto-revert a live database change without a human in the loop).

## 3. Cost/GPU-aware scheduler plugin

**Purpose:** place workload pods with a scoring function that accounts for cost and fragmentation, not just default bin packing.

**Stack:** Go, Kubernetes scheduler framework (a scoring plugin, not a full custom scheduler), Prometheus for node/cost metrics, AWS spot pricing API.

**Design problems:**

- **Three-way scoring tradeoff.** Cost (spot vs. on-demand), fragmentation (does this placement leave usable gaps on other nodes), and preemption risk (spot nodes can be reclaimed) pull against each other. The scoring function's weighting is documented explicitly in code comments and in this doc, this is the part that has to be defensible, not just functional.
- **No workload starvation.** Latency-sensitive pods must never land on cheap-but-reclaimable nodes regardless of score. Solved with a workload-class filter that excludes critical pods from spot placement before scoring even runs.

## 4. Progressive delivery controller

**Purpose:** canary rollout for any deployment made through the platform, with automatic rollback based on live metrics.

**Stack:** Go, Istio or Linkerd (traffic shifting), Prometheus (metric source), custom CRD for rollout config.

**Design problems:**

- **Statistical rollback decision.** A raw error-rate threshold false-triggers on noisy-but-healthy canaries and under-reacts to slow-building real regressions. Solved with a minimum sample size gate plus a statistical significance check before any rollback fires.
- **Traffic-shift step sizing.** Fixed percentage steps are naive. Step size shrinks as confidence in the canary grows and widens back out on any anomaly.

## 5. Chaos and resilience scoring module

**Purpose:** on-demand or scheduled controlled failure injection against a target service, producing an automated resilience score against declared SLOs.

**Stack:** Go, Kubernetes admission/controller APIs, Prometheus, OpenTelemetry, `tc`/`iptables` for network fault injection (latency, partition), pod-kill via the Kubernetes API.

**Design problems:**

- **Blast-radius containment.** Every experiment has a hard scope (namespace, label selector, max percentage of replicas) and a kill switch that reverts all injected faults immediately on SLO breach. This is a safety system before it's a testing tool, treated accordingly.
- **Abort logic.** The SLO breach threshold that auto-aborts an experiment is defined per-service, not global, since acceptable degradation varies by service criticality.

## 6. Audit, compliance, and RBAC service

**Purpose:** immutable, queryable audit trail and access control sitting in front of the whole platform.

**Stack:** Java, Spring Boot, Spring Security (OAuth2/JWT), Kafka (event stream), JPA/Hibernate over PostgreSQL, JUnit/Mockito.

**Design problems:**

- **Tamper-evidence.** Every entry is hash-chained to the previous one, so a modified record breaks the chain and is detectable on verification rather than just logged and trusted.
- **Exactly-once processing.** A Kafka redelivery must not create a duplicate entry or a gap. Solved with idempotency keys derived from the event ID plus a unique constraint at the database layer.
- **RBAC boundary.** This service enforces *who can submit a request*. OPA/Kyverno at the admission layer enforces *what a request is allowed to contain*. One source of truth per concern, no duplicated policy logic between the two.
- **Query performance at scale.** Indexed on actor, resource, and timestamp range; the table is partitioned by time once volume warrants it.

See [`API_REFERENCE.md`](API_REFERENCE.md#audit-event-schema) for the event schema and endpoint list.

## Cross-cutting: observability

Prometheus and Grafana are owned centrally, every subsystem exports metrics to the same instance rather than standing up its own. OpenTelemetry traces span the full request flow (Section "Request flow" above), so a single request can be followed from submission through policy check, provisioning, scheduling, and deployment in one trace.
