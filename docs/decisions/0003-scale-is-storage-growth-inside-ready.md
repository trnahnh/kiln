# ADR-0003: Scaling means storage growth and stays inside the Ready phase

**Status:** Accepted, 2026-09-05

## Context

Phase 1's exit criterion includes scaling a database and a concurrent scale+backup test. The `TenantDatabase` spec has no `replicas`; its only growable field is `storageGB`. The phase set in [`API_REFERENCE.md`](../API_REFERENCE.md#tenantdatabase-crd) is fixed at `Provisioning`, `Ready`, `Backing Up`, `Restoring`, `Failed`, with no phase for a scale in progress. The alternatives were adding `replicas` with streaming replication, mapping `tier` to pod resources, adding a `Scaling` phase, or reusing `Provisioning` for any spec change.

## Decision

A scale-up is an increase of `spec.storageGB`. The operator owns the data `PersistentVolumeClaim` directly (not through a `volumeClaimTemplate`) and patches its requested size. The CRD rejects a shrink.

While the resize is in flight the phase remains `Ready`; the `Ready` condition is False with reason `Scaling` and `status.observedGeneration` lags `metadata.generation`. The claim is settled when its spec carries the requested size and it reports no `Resizing` or `FileSystemResizePending` condition. Backups and restores start only from `Ready` with the `Ready` condition True, and a spec change is applied only in `Ready`, so a scale and an operation never overlap.

## Consequences

- The documented phase set and transition list are unchanged.
- The collision test asserts on the PVC's actual spec size and the Job's completion order, not on `status.phase`.
- On provisioners that never report capacity (kind's `local-path`), the resize settles as soon as the API server accepts it; the StorageClass must allow expansion or the operator reports `ScaleFailed` and keeps retrying.
- Compute scaling (`tier` to resources) and read replicas are not covered; either would supersede this ADR.
