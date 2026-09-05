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
