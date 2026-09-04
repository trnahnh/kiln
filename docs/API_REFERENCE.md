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

**Phase transitions:** `Provisioning -> Ready`, `Ready -> Backing Up -> Ready`, `Ready -> Restoring -> Ready`, any phase `-> Failed` on unrecoverable error. Transitions are enforced by the reconciler's state machine; a conflicting transition (e.g. a scale request arriving mid-`Backing Up`) is rejected and requeued, never interleaved.

## Provisioning claim (Crossplane Composite Resource)

Submitted by a developer through the golden path; consumed by the Provisioning/GitOps layer.

```yaml
apiVersion: platform.internal/v1alpha1
kind: DatabaseClaim
metadata:
  name: checkout-db
  namespace: team-checkout
spec:
  compositionRef:
    name: standard-postgres     # or a custom composition
  parameters:
    tier: standard
    storageGB: 20
    tags:
      team: checkout
      costCenter: eng-platform
```

Requests using `compositionRef: standard-postgres` inherit platform defaults (instance class, backup retention, network placement). Requests using a `custom` composition must specify those fields explicitly and are subject to stricter OPA/Kyverno review.

## CanaryRollout CRD

Owned by the Progressive delivery controller.

```yaml
apiVersion: platform.internal/v1
kind: CanaryRollout
metadata:
  name: checkout-service-rollout
spec:
  targetDeployment: checkout-service
  metricProvider: prometheus
  successCriteria:
    errorRateMax: 0.01
    latencyP99MaxMs: 300
    minSampleSize: 500
  stepPercentages: [5, 20, 50, 100]
status:
  phase: Analyzing        # Progressing | Analyzing | Promoting | RolledBack | Succeeded
  currentStep: 1
  lastAnalysisResult: Pass
```

## ChaosExperiment CRD

Owned by the Chaos/resilience module.

```yaml
apiVersion: platform.internal/v1
kind: ChaosExperiment
metadata:
  name: checkout-service-partition-test
spec:
  target:
    namespace: team-checkout
    labelSelector: app=checkout-service
    maxReplicaPercentage: 30
  faultType: network-partition   # pod-kill | network-partition | latency-injection | resource-exhaustion
  duration: 5m
  abortOnSLOBreach:
    errorRateMax: 0.05
    latencyP99MaxMs: 1000
status:
  phase: Running          # Scheduled | Running | Aborted | Completed
  resilienceScore: null
  abortReason: null
```

## Audit event schema

Emitted by every subsystem to the Kafka event stream, consumed by the Audit/RBAC service.

```json
{
  "eventId": "uuid",
  "actor": "user@company.com",
  "action": "PROVISION_REQUEST | POLICY_DENY | DEPLOY | ROLLBACK | CHAOS_EXPERIMENT",
  "resource": "TenantDatabase/team-checkout-db",
  "timestamp": "2026-09-04T18:00:00Z",
  "prevHash": "sha256 of previous entry",
  "hash": "sha256 of this entry"
}
```

`prevHash`/`hash` form the tamper-evidence chain described in [`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md#6-audit-compliance-and-rbac-service). Verifying the chain means recomputing each entry's hash from its content plus `prevHash` and confirming it matches the stored `hash`.

## Audit/RBAC service REST endpoints

| Method | Path | Purpose | Auth |
|---|---|---|---|
| `POST` | `/v1/requests` | Submit a provisioning/deploy/chaos request (thin API alternative to applying a CRD directly) | Bearer JWT |
| `GET` | `/v1/audit?actor=&resource=&from=&to=` | Query the audit trail by actor, resource, and time range | Bearer JWT, `audit:read` role |
| `GET` | `/v1/audit/verify` | Trigger a full hash-chain verification pass, returns any broken links | Bearer JWT, `audit:admin` role |
| `GET` | `/healthz` | Liveness probe | None |

All write paths (`POST /v1/requests`) are also mirrored as Kafka producers internally; the REST endpoint is a convenience wrapper for developers who don't want to apply a CRD directly, not a bypass of the event stream.

## Error codes

| Code | Meaning |
|---|---|
| `POLICY_DENIED` | Request rejected by OPA/Kyverno before reaching provisioning. Response includes the specific rule that failed. |
| `RECONCILE_CONFLICT` | A reconcile step was rejected because it conflicts with an in-progress operation on the same resource (e.g. scale request during backup). |
| `SLO_BREACH_ABORT` | A chaos experiment or canary rollout was auto-aborted due to a defined SLO breach. |
| `AUDIT_CHAIN_BROKEN` | Hash-chain verification found a tampered or missing entry. |
