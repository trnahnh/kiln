# ADR-0002: One-shot backup and restore are requested through annotations

**Status:** Accepted, 2026-09-05

## Context

[`ROADMAP.md`](../ROADMAP.md) Phase 1 requires backing up and restoring a Postgres instance "through the CRD alone". The `TenantDatabase` schema in [`API_REFERENCE.md`](../API_REFERENCE.md#tenantdatabase-crd) has a `backupSchedule` for recurring backups but no field that requests an immediate backup or a restore. Both are one-shot actions: they run once, finish, and leave nothing to reconcile. The alternatives were new spec fields (`spec.restoreFrom`, a one-shot backup flag) or separate `TenantDatabaseBackup` / `TenantDatabaseRestore` kinds with their own controllers.

## Decision

One-shot actions are requested by annotating the `TenantDatabase`:

- `platform.internal/backup: now` starts a backup.
- `platform.internal/restore-from: <backupID|latest>` starts a restore.

The operator consumes the annotation when it creates the Job and removes it in the same reconcile. The spec stays declarative and unchanged from the documented schema. The annotation contract is documented in `API_REFERENCE.md`, which owns the CRD surface.

## Consequences

- No schema change and no additional CRDs or controllers in Phase 1.
- An action is only accepted from `Ready` with the `Ready` condition True; otherwise it stays on the object and is recorded as `RECONCILE_CONFLICT` until it can run, so an annotation is never silently dropped.
- One-shot state lives nowhere in `spec`, so `kubectl apply` of a stored manifest cannot accidentally re-trigger a restore.
- Backup history is not modelled as objects. If a later phase needs per-backup status or retention policy, separate Backup kinds supersede this ADR.
