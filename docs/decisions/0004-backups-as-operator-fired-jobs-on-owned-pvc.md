# ADR-0004: Backups are operator-fired Jobs writing to an owned PersistentVolumeClaim

**Status:** Accepted, 2026-09-05

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#1-kubernetes-operator-stateful-workload-lifecycle) makes `status.phase` the sole source of truth for in-flight operations, and the CLAUDE.md invariants forbid interleaving a scale with a backup. `spec.backupSchedule` is a cron expression. The alternatives for firing it were a Kubernetes `CronJob` owned by the operator, or the operator evaluating the schedule itself. For where dumps land, the alternatives were a second PVC, a directory on the data volume, or an object store such as MinIO. Phase 1 runs on a `kind` cluster with `local-path` storage and no snapshot controller.

## Decision

The reconciler evaluates the cron expression itself. When a backup falls due, or is requested through the annotation of ADR-0002, it transitions `Ready -> Backing Up` and creates a `batch/v1` Job that runs `pg_dump -Fc` against the instance's Service and writes `<timestamp>.dump` to a `<name>-backups` PVC. The PVC and every Job carry a controller owner reference to the `TenantDatabase`. Restore is the mirror Job running `pg_restore --clean --if-exists` from a named dump or the newest one. `status.lastBackupTime` is the timestamp of the last successful dump.

A failed backup Job returns the phase to `Ready` with a `BackupFailed` condition because the database is untouched. A failed restore Job moves to `Failed`, which is terminal, because the data state is unknown.

## Consequences

- Every backup passes through the state machine, so a scale arriving mid-backup is detected and deferred as `RECONCILE_CONFLICT`. A `CronJob` would have run outside the phase machine and made the invariant unenforceable.
- No extra components: works on kind with only the default provisioner.
- Deleting the `TenantDatabase` deletes its backups too, by garbage collection. Retaining backups past the CR's life needs a different owner and a superseding ADR.
- Dumps accumulate on the backups PVC; there is no retention pruning in Phase 1.
- The shell run by the Jobs is a constructor dependency of the reconciler, so tests can hold a backup open deterministically without a test-only code path in production.
