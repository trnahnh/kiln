# API Reference

## TenantDatabase CRD

Owned by the Operator ([`SYSTEM_DESIGN.md#1-kubernetes-operator`](SYSTEM_DESIGN.md#1-kubernetes-operator-stateful-workload-lifecycle)).

```yaml
apiVersion: platform.internal/v1
kind: TenantDatabase
metadata:
  name: team-checkout-db
spec:
  engine: postgres        # postgres | redis
  version: "16"
  storageGB: 20
  backupSchedule: "0 3 * * *"   # cron
  tier: standard           # standard | custom
status:
  phase: Provisioning      # Provisioning | Ready | Backing Up | Restoring | Failed
  lastBackupTime: "2026-09-01T03:00:00Z"
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2026-09-01T02:58:11Z"
```

**Phase transitions:** `Provisioning -> Ready`, `Ready -> Backing Up -> Ready`, `Ready -> Restoring -> Ready`, any phase `-> Failed` on unrecoverable error. Transitions are enforced by the reconciler's state machine; a conflicting transition (e.g. a scale request arriving mid-`Backing Up`) is rejected and requeued, never interleaved. `Failed` is terminal: recovery is delete and recreate. A failed backup is not unrecoverable and returns to `Ready`; a failed restore is and goes to `Failed`.

**Validation:** `engine` and `version` are immutable; `storageGB` may grow but never shrink; `tier` defaults to `standard`; `backupSchedule` is optional (empty disables scheduled backups). `engine: redis` is accepted by the schema and sent to `Failed` with reason `UnsupportedEngine` until a Redis implementation lands.

**Scaling** is growth of `storageGB` ([ADR-0003](decisions/0003-scale-is-storage-growth-inside-ready.md)). The phase stays `Ready`; the `Ready` condition is `False` with reason `Scaling` until the volume resize settles, and `status.observedGeneration` catches up to `metadata.generation` once the spec is fully applied.

**One-shot actions** are requested with annotations, consumed by the operator when the Job starts ([ADR-0002](decisions/0002-one-shot-actions-via-annotations.md)):

| Annotation | Value | Effect |
|---|---|---|
| `platform.internal/backup` | `now` | `Ready -> Backing Up`; runs a `pg_dump` Job. |
| `platform.internal/restore-from` | `<backupID>` or `latest` | `Ready -> Restoring`; runs a `pg_restore` Job from that dump. A backup ID is the UTC timestamp `YYYYMMDDTHHMMSSZ` recorded in `status.lastBackupTime`. |

Both are accepted only from `Ready` with the `Ready` condition `True`; otherwise they stay on the object and `Progressing` reports `RECONCILE_CONFLICT` until they can run.

**Conditions:** `Ready` (database available; reasons `Provisioning`, `Scaling`, `Reconciled`, `UnsupportedEngine`, `ProvisionFailed`, `RestoreFailed`) and `Progressing` (work in flight or its last outcome; reasons `Provisioning`, `Scaling`, `ScaleFailed`, `BackingUp`, `Restoring`, `Reconciled`, `BackupFailed`, `RestoreFailed`, `RECONCILE_CONFLICT`). A `RECONCILE_CONFLICT` also emits one Warning event per conflict episode.

**Operator-managed resources** in the CR's namespace, all with a controller owner reference to it ([ADR-0004](decisions/0004-backups-as-operator-fired-jobs-on-owned-pvc.md)):

| Resource | Name | Purpose |
|---|---|---|
| Secret | `<name>-credentials` | `POSTGRES_PASSWORD` for the `postgres` superuser. |
| Service | `<name>` | ClusterIP on 5432. |
| StatefulSet | `<name>` | One `postgres:<version>` pod; pod `<name>-0`. |
| PersistentVolumeClaim | `<name>-data` | Data volume, sized by `storageGB`. |
| PersistentVolumeClaim | `<name>-backups` | Dump files named `<backupID>.dump`. |
| Job | `<name>-backup-<id>`, `<name>-restore-<id>` | One per operation; labels `platform.internal/tenantdatabase`, `platform.internal/operation`. |

Deleting the `TenantDatabase` garbage-collects all of them, backups included.

## Provisioning claim (Crossplane Composite Resource)

Submitted by a developer through the golden path; consumed by the Provisioning/GitOps layer.

```yaml
apiVersion: platform.internal/v1alpha1
kind: DatabaseClaim
metadata:
  name: checkout-db
  namespace: team-checkout
spec:
  crossplane:                    # Crossplane's own fields; optional, defaults to standard-postgres
    compositionRef:
      name: standard-postgres    # or a custom composition
  parameters:
    tier: standard               # standard | custom
    storageGB: 20
    tags:
      team: checkout
      costCenter: eng-platform
```

The claim is a namespaced Crossplane v2 composite resource ([ADR-0005](decisions/0005-databaseclaim-is-a-namespaced-crossplane-v2-xr.md)); `spec.crossplane` holds Crossplane's selection fields and `spec.parameters` the request. Requests using `standard-postgres` inherit platform defaults (Postgres 16, daily 03:00 backup, standard tier) and yield a `TenantDatabase` of the same name in the same namespace, owned by the claim, labelled `platform.internal/team` and `platform.internal/cost-center` from `tags`. Requests using a `custom` composition must specify those fields explicitly and are subject to stricter OPA/Kyverno review; no custom composition exists yet, and policy rejects `tier: custom` until one does.

**Admission rules** ([ADR-0006](decisions/0006-org-rules-as-fail-closed-kyverno-validating-policies.md), defined in `gitops/policies`): `tags.team` and `tags.costCenter` are mandatory; `storageGB` is at most 100 on both the claim and any `TenantDatabase`; only the standard tier and composition are accepted. A rejected request never creates an object; the error message reads `POLICY_DENIED rule=<rule>: <reason>`.

## CanaryRollout CRD

Owned by the Progressive delivery controller. Namespaced; one per Deployment. The Deployment named by `targetDeployment` is the canary and must sit behind a Service of the same name; the controller creates `<name>-primary` (Deployment and Service), `<name>-canary` (Service) and the Istio VirtualService, all owned by the CR ([ADR-0013](decisions/0013-the-target-deployment-is-the-canary-and-the-controller-owns-primary.md)). A change to the target's pod template starts a rollout.

```yaml
apiVersion: platform.internal/v1
kind: CanaryRollout
metadata:
  name: checkout-service-rollout
  namespace: team-checkout
spec:
  targetDeployment: checkout-service
  metricProvider: prometheus      # the only provider; Istio's destination-reported metrics
  successCriteria:
    errorRateMax: 0.01            # null hypothesis of the error-rate test
    latencyP99MaxMs: 300          # null hypothesis: at most 1% of requests slower than this
    minSampleSize: 500            # requests since the current checkpoint before any decision
  stepPercentages: [5, 20, 50, 100]   # ascending checkpoints ending at 100
  analysis:                       # optional; defaults shown (ADR-0014)
    interval: 15s                 # how often metrics are read and the test advanced
    maxStepDuration: 30m          # an undecided checkpoint rolls back after this
    alpha: 0.05                   # false-rollback ceiling
    beta: 0.1                     # missed-regression ceiling at the regression magnitude
    regressionFactor: 2           # alternative hypothesis = this multiple of the limit
    drainGrace: 10s               # canary keeps running this long after traffic leaves it; 0s parks it at once
status:
  phase: Analyzing        # Initializing | Progressing | Analyzing | Promoting | Draining | RolledBack | Succeeded
  currentStep: 1          # 1-based index of the last checkpoint reached
  canaryWeight: 12        # mirrors the VirtualService; informational, never the proof
  lastAnalysisResult: Pending   # Pending | Pass | Fail
  reason: Analyzing       # terminal: Promoted | RegressionDetected | Inconclusive | MetricsUnavailable | CanaryUnavailable
  targetReplicas: 3       # what the target had before being parked at zero
  observedTemplateHash: 9a23219f06d5f0d2
  promotedTemplateHash: 1c1d1f6e5c0a4e83
  trafficFlippedAt: null  # set while Draining: when the VirtualService went to 100% primary
  analysis:               # persisted test state so a restarted controller resumes
    checkpoint: 1
    errors:   {cumulative: 0.42, sinceCheckpoint: -1.9}
    latency:  {cumulative: 0,    sinceCheckpoint: -2.3}
    samplesSinceCheckpoint: 640
    totalSamples: 2210
    confidence: 0.84
    shrink: 0
    anomalies: 1
    checkpointStartedAt: "2026-09-05T18:24:28Z"
    lastTickAt: "2026-09-05T18:25:13Z"
    lastCounters: {requests: 2210, errors: 4, slow: 1, at: "2026-09-05T18:25:13Z"}
  conditions:             # Ready (True only when idle on a promoted version), Progressing
    - type: Ready
      status: "False"
      reason: Analyzing
```

Phases: `Initializing` clones primary and parks the target; `Succeeded` and `RolledBack` are idle, all traffic on primary, target at zero replicas; `Progressing` waits for the canary pods and a baseline metric snapshot; `Analyzing` runs the sequential test every `interval`; `Promoting` copies the accepted template onto primary and hands traffic back; `Draining` (after a promotion or a rollback) has already routed everything to primary and keeps the canary running for `drainGrace` so clients whose sidecars still hold the old routes are not sent to vanished pods, then parks it. Labels the controller manages: `platform.internal/canary-role` (`primary` or `canary`, on pod templates and Service selectors) and `platform.internal/canary-rollout` (the CR name). Events: `RolloutStarted`, `TrafficShifted`, `Promoting`, `Promoted`, and a Warning `RolledBack` carrying the reason and criterion.

## ChaosExperiment CRD

Owned by the Chaos/resilience module ([`SYSTEM_DESIGN.md#5`](SYSTEM_DESIGN.md#5-chaos-and-resilience-scoring-module)).

```yaml
apiVersion: platform.internal/v1
kind: ChaosExperiment
metadata:
  name: checkout-service-partition-test
  namespace: team-checkout
spec:
  target:
    namespace: team-checkout      # optional; must equal the experiment's namespace, unset means it
    labelSelector: app=checkout-service
    maxReplicaPercentage: 30      # floored to whole pods; an experiment that floors to zero is rejected
  faultType: network-partition    # pod-kill | network-partition | latency-injection | resource-exhaustion
  duration: 5m
  abortOnSLOBreach:               # required; cannot be disabled per experiment
    errorRateMax: 0.05
    latencyP99MaxMs: 1000         # a window breaches if more than 1% of requests are slower
  fault:                          # optional; per-type parameters, defaults shown
    latencyMs: 500                # latency-injection: delay added to the pod's egress
    jitterMs: 50                  # latency-injection
    cpuPercent: 100               # resource-exhaustion: share of the container's CPU limit
    memoryMiB: 0                  # resource-exhaustion: memory allocated in the container's cgroup
    interval: 30s                 # pod-kill: how often a fresh selection of up to the cap is deleted
  analysis:                       # optional; defaults shown
    interval: 5s                  # how often the SLOs are polled
    window: 15s                   # length of one metric window; matches the Prometheus scrape
    minSampleSize: 20             # requests a window must hold before it is judged
    recoveryWindows: 4            # windows observed after the fault is removed, for the recovery score
status:
  phase: Running                  # Scheduled | Running | Aborted | Completed
  reason: Running
  abortReason: null               # SLOBreach | MetricsUnavailable | InjectionFailed when aborted
  resilienceScore: null           # 0 to 100 once ended; 0 when aborted (ADR-0016)
  startedAt: "2026-09-05T18:00:00Z"
  faultEndsAt: "2026-09-05T18:05:00Z"   # startedAt + duration; fault removed here unless aborted earlier
  faultEndedAt: null              # when every injected fault was confirmed gone
  leaseExpiresAt: "2026-09-05T18:00:15Z"  # renewed each interval; agents revert once it lapses
  kills: 0                        # pod-kill count
  targets:                        # the blast-radius record; length never exceeds floor(cap x matching)
    - {pod: checkout-service-abc, uid: "...", node: node-1, state: Selected}
  analysis:                       # persisted so a restarted controller resumes
    faultWindows: 8
    headroomTotal: 7.4
    worstErrorRate: 0.012
    worstSlowFraction: 0.004
    recoveredAfter: 0             # index of the first post-fault window within SLO; unset until one is
  conditions:
    - {type: Ready, status: "True", reason: Running}
    - {type: FaultsCleared, status: "False", reason: FaultsLive}
```

**Phases:** `Scheduled` (admitted, waiting for pods, a baseline metric snapshot, or an overlapping experiment on the same pods) `-> Running` (faults live, lease renewed each interval) `-> Completed` (the fault ran its course; score set) or `-> Aborted` (an SLO breach, lost metrics, or a fault that could not be injected; faults reverted, score 0). `FaultsCleared` becomes true only once the lease has lapsed, so every agent has provably reverted. A `ChaosExperiment` carries a finalizer and is held on delete until the lease lapses. The spec is immutable; change an experiment by creating a new one. Blast radius, abort, and score are enforced by the agent and controller as described in [ADR-0015](decisions/0015-chaos-agent-enforces-blast-radius-with-a-lease-dead-man-switch.md) and [ADR-0016](decisions/0016-resilience-score-is-slo-headroom-plus-recovery.md); the abort uses the `SLO_BREACH_ABORT` error code.

## Scheduler placement contract

Owned by the Scheduler plugin ([`SYSTEM_DESIGN.md#3`](SYSTEM_DESIGN.md#3-costgpu-aware-scheduler-plugin)). Pods opt into the cost-aware scheduler with `spec.schedulerName: kiln-scheduler`.

**Pod label** ([ADR-0009](decisions/0009-workload-class-label-gates-spot-placement.md)):

| Label | Values | Default when absent |
|---|---|---|
| `kiln.platform.internal/workload-class` | `latency-sensitive`, `standard`, `batch` | `latency-sensitive` (never placed on spot) |

**Node labels** ([ADR-0010](decisions/0010-node-economics-as-labels-with-pluggable-price-sources.md)):

| Label | Values |
|---|---|
| `kiln.platform.internal/capacity-type` | `spot`, `on-demand` |
| `kiln.platform.internal/hourly-cost` | decimal USD per hour |
| `kiln.platform.internal/preemption-risk` | optional, 0 to 1; default 0.05 for spot, ignored for on-demand |

A node missing the contract is treated as on-demand at the highest known hourly cost. On EKS the `aws` price source derives the same facts from `eks.amazonaws.com/capacityType` or `karpenter.sh/capacity-type`, `node.kubernetes.io/instance-type`, zone and region.

**Plugin arguments** (`KubeSchedulerConfiguration` `pluginConfig`, name `CostAware`): `weights.cost`, `weights.fragmentation`, `weights.preemption`, integers summing to 100; defaults 50/30/20.

## Audit event schema

Every subsystem publishes to the Kafka topic `kiln.audit` (one partition, record key = `resource`); the Audit/RBAC service is the only consumer and the only writer of the audit table ([ADR-0018](decisions/0018-the-audit-service-alone-computes-the-hash-chain.md)). A publisher sends the **wire event**; the service adds the chain fields when it stores the **entry**.

**Wire event** (the Kafka record value, JSON):

```json
{
  "eventId": "6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10",
  "actor": "user@company.com",
  "action": "PROVISION",
  "resource": "TenantDatabase/team-checkout/checkout-db",
  "timestamp": "2026-09-04T18:00:00.000000Z",
  "details": {"outcome": "Ready"}
}
```

| Field | Meaning |
|---|---|
| `eventId` | UUID. Publishers derive it deterministically (UUID v5) from the resource, action and the transition's own key, so a retried reconcile or a redelivered record carries the same id and is stored once. |
| `actor` | Who caused the action: the JWT subject on the REST path, the `platform.internal/requested-by` annotation on a CR the service applied, otherwise `system:<controller>`. |
| `action` | One of the table below. |
| `resource` | `<Kind>/<namespace>/<name>`. |
| `timestamp` | RFC 3339 UTC; stored at microsecond precision. |
| `details` | JSON object, may be empty. `outcome` names the result of the action; other keys are per action. Part of the hashed content. |

| Action | Publisher | `details` |
|---|---|---|
| `PROVISION_REQUEST` | Audit service, `POST /v1/requests` | `outcome: Received`, `kind` |
| `POLICY_DENY` | Audit service, `POST /v1/requests` | `outcome: Denied`, `rule`, `reason` |
| `PROVISION` | Operator | `outcome: Ready \| Failed`, `reason` |
| `BACKUP`, `RESTORE` | Operator | `outcome: Started \| Succeeded \| Failed`, `backupId` |
| `SCALE` | Operator | `outcome: Applied \| Rejected`, `from`, `to` (storage) |
| `SCHEDULE` | Scheduler plugin, PostBind | `outcome: Bound`, `node`, `workloadClass` |
| `DEPLOY` | Delivery controller | `outcome: Started \| Promoted`, `templateHash` |
| `ROLLBACK` | Delivery controller | `outcome: RolledBack`, `reason`, `criterion`, `templateHash` |
| `CHAOS_EXPERIMENT` | Chaos controller | `outcome: Started \| Completed \| Aborted`, `faultType`, `targets`, `resilienceScore`, `abortReason` |

**Stored entry** (what `GET /v1/audit` returns): the wire event plus `seq`, `prevHash` and `hash`. The chain and the table are defined in [`DATA_MODEL.md`](DATA_MODEL.md). Verifying the chain means recomputing each entry's hash from its content plus `prevHash`, confirming it matches the stored `hash`, and confirming its `prevHash` equals the previous entry's `hash`.

## Audit/RBAC service REST endpoints

Roles are read from the JWT's `roles` claim (an array of strings).

| Method | Path | Purpose | Auth |
|---|---|---|---|
| `POST` | `/v1/requests` | Submit a `DatabaseClaim`, `CanaryRollout` or `ChaosExperiment` manifest (JSON body) on the caller's behalf: the service applies it, stamped `platform.internal/requested-by: <subject>`, and publishes `PROVISION_REQUEST`; an admission rejection publishes `POLICY_DENY` and returns `422` with `POLICY_DENIED` | Bearer JWT, `requests:submit` role |
| `GET` | `/v1/audit?actor=&resource=&from=&to=&limit=` | Query the audit trail by actor, resource, and time range, ordered by `seq`; `limit` defaults to 100, at most 1000 | Bearer JWT, `audit:read` role |
| `GET` | `/v1/audit/verify` | Full hash-chain verification pass; `{"ok": true, "entries": n}` or `{"ok": false, "code": "AUDIT_CHAIN_BROKEN", "brokenLinks": [{"seq", "eventId", "reason"}]}` | Bearer JWT, `audit:admin` role |
| `GET` | `/healthz` | Liveness probe | None |

`POST /v1/requests` is mirrored as a Kafka producer internally: the service publishes its own events to `kiln.audit` and stores them only when it consumes them like any other subsystem's. The REST endpoint is a convenience wrapper for developers who don't want to apply a CRD directly, not a bypass of the event stream.

## Error codes

| Code | Meaning |
|---|---|
| `POLICY_DENIED` | Request rejected by OPA/Kyverno before reaching provisioning. Response includes the specific rule that failed. |
| `RECONCILE_CONFLICT` | A reconcile step was rejected because it conflicts with an in-progress operation on the same resource (e.g. scale request during backup). |
| `SLO_BREACH_ABORT` | A chaos experiment or canary rollout was auto-aborted due to a defined SLO breach. |
| `AUDIT_CHAIN_BROKEN` | Hash-chain verification found a tampered or missing entry. |
