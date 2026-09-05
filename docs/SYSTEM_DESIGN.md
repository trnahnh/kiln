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

Decisions that shaped the Phase 2 implementation: [ADR-0005](decisions/0005-databaseclaim-is-a-namespaced-crossplane-v2-xr.md) (the claim is a namespaced Crossplane v2 composite composing the `TenantDatabase` directly), [ADR-0006](decisions/0006-org-rules-as-fail-closed-kyverno-validating-policies.md) (fail-closed Kyverno ValidatingPolicies on claim and `TenantDatabase`), [ADR-0007](decisions/0007-argocd-bootstraps-the-platform-with-drift-policy-per-application.md) (ArgoCD app-of-apps bootstrap, drift policy per Application class).

## 3. Cost/GPU-aware scheduler plugin

**Purpose:** place workload pods with a scoring function that accounts for cost and fragmentation, not just default bin packing.

**Stack:** Go, Kubernetes scheduler framework (a scoring plugin, not a full custom scheduler), Prometheus for node/cost metrics, AWS spot pricing API.

**Design problems:**

- **Three-way scoring tradeoff.** Cost (spot vs. on-demand), fragmentation (does this placement leave usable gaps on other nodes), and preemption risk (spot nodes can be reclaimed) pull against each other. The scoring function's weighting is documented explicitly in code comments and in this doc, this is the part that has to be defensible, not just functional.
- **No workload starvation.** Latency-sensitive pods must never land on cheap-but-reclaimable nodes regardless of score. Solved with a workload-class filter that excludes critical pods from spot placement before scoring even runs.

**Scoring weighting.** The `CostAware` plugin scores each feasible node as `50 × cost + 30 × fragmentation + 20 × preemption`, each term in [0,1], giving [0,100]. Cost leads because lower spend is the subsystem's stated purpose; it is 1 for the cheapest candidate and 0 for the most expensive, so it only expresses a preference when candidates differ in price. Fragmentation is second so savings are not undone by stranding on-demand capacity in unusable slivers: it is the node's post-placement utilisation of the pod's most demanding resource (CPU, memory or `nvidia.com/gpu`), which packs pods rather than spreading them, halved for a pod that would occupy a GPU node without using its GPUs. Preemption is last because the class filter already removes the catastrophic case; the residual term (1 minus the node's reclaim risk, always 1 for on-demand) only orders spot nodes among themselves and can never outweigh the cost lead on its own. The weights are plugin arguments in the scheduler profile and must sum to 100; the defaults live in `scheduler-plugin/internal/scoring` next to the formula. In the `kiln-scheduler` profile the upstream `NodeResourcesFit` and `NodeResourcesBalancedAllocation` score plugins are disabled because they spread pods and would fight the fragmentation term. Decisions: [ADR-0009](decisions/0009-workload-class-label-gates-spot-placement.md) (workload-class label and filter-before-score), [ADR-0010](decisions/0010-node-economics-as-labels-with-pluggable-price-sources.md) (node economics contract and price sources).

## 4. Progressive delivery controller

**Purpose:** canary rollout for any deployment made through the platform, with automatic rollback based on live metrics.

**Stack:** Go, Istio in sidecar mode (traffic shifting through a weighted VirtualService), Prometheus (metric source, Istio's destination-reported request counters and latency histogram), `CanaryRollout` CRD for rollout config.

**Design problems:**

- **Statistical rollback decision.** A raw error-rate threshold false-triggers on noisy-but-healthy canaries and under-reacts to slow-building real regressions, and a significance test re-run at every analysis interval inflates its own false-positive rate with every look. Solved with a minimum sample size gate plus a sequential probability ratio test per criterion (error rate, and the fraction of requests slower than the p99 limit), whose per-window evidence is capped so neither one burst nor one quiet window can decide a rollout. Rollback evidence accumulates for the whole rollout as a CUSUM, so slow-building regressions still cross the bound; acceptance is re-earned on fresh samples at every checkpoint.
- **Traffic-shift step sizing.** Fixed percentage steps are naive. `stepPercentages` are checkpoints that must each be accepted; between them the canary weight is the checkpoint plus the confidence earned so far times the distance to the next, so sub-steps grow as confidence grows, and a window whose evidence moved toward the regression hypothesis holds traffic and halves the next sub-step. The first checkpoint is entered outright because no traffic means no evidence.
- **Ground truth.** The controller never trusts its own status. What proves a rollback is the VirtualService's weights, the canary pods being gone and primary pods still on the old template, and real requests through the mesh returning healthy again.
- **No metrics, no traffic.** Analysis starts only once a baseline counter snapshot has been read, and a checkpoint that cannot decide within `maxStepDuration` rolls back rather than waiting or promoting. The first CI run of the Phase 4 end-to-end test had no Prometheus at all (the job never applied the observability manifests) and the controller held the canary at 0% for the entire window; reproduced afterwards on a kind cluster with Prometheus scaled to zero, the same path went on to the real abort: 70 seconds after the template change the VirtualService was still 100% primary, the canary was parked at zero replicas with no pods, and the rollout reported `MetricsUnavailable`. That is the abort path exercised under production conditions, not only in the simulation suite.

**Known limit.** A regression that builds more slowly than the rollout completes can be promoted: once the last checkpoint is accepted the canary is the new primary and no further analysis runs. This is inherent to any finite canary window, not a defect; a longer schedule or a larger `minSampleSize` widens the window at the cost of rollout time.

**Rollout model.** The user's Deployment is the canary and always holds the desired version; the controller owns a `<name>-primary` Deployment that serves stable traffic, the `<name>-primary` and `<name>-canary` Services, and the VirtualService on the user's own Service host. A change to the target's pod template starts a rollout, the accepted template is copied onto primary at the end, and the target is parked at zero replicas while idle or after a rollback. Parking waits a drain grace after the traffic flip (`drainGrace`, default 10 s): the API server shows the new route a second or two before every client sidecar has it, and a canary removed in that window fails the requests still routed to it, which the end-to-end test measures with an uninterrupted load run spanning the flip. Decisions: [ADR-0011](decisions/0011-istio-virtualservice-is-the-canary-traffic-router.md) (Istio and the VirtualService router), [ADR-0012](decisions/0012-platform-app-delivers-every-module-crd.md) (the CRD is delivered with the rest of the platform), [ADR-0013](decisions/0013-the-target-deployment-is-the-canary-and-the-controller-owns-primary.md) (primary ownership and the rollout trigger), [ADR-0014](decisions/0014-sequential-probability-ratio-test-decides-rollback.md) (the sequential test, its bounds and defaults, and confidence-sized sub-steps).

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
