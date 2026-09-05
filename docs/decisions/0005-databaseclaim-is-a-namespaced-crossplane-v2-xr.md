# ADR-0005: DatabaseClaim is a namespaced Crossplane v2 composite resource that composes a TenantDatabase directly

**Status:** Accepted, 2026-09-05

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#2-provisioning-and-gitops-layer) puts a golden-path abstraction between the developer and the operator: a developer asks for "a standard Postgres database" and platform defaults fill the rest. [`API_REFERENCE.md`](../API_REFERENCE.md#provisioning-claim-crossplane-composite-resource) documents that request as a namespaced `DatabaseClaim`. Crossplane 2.x lets a composite resource be namespaced and compose any Kubernetes object, while its legacy mode keeps the 1.x split of a namespaced claim, a cluster-scoped composite, and a provider (provider-kubernetes) to wrap in-cluster objects.

## Decision

`DatabaseClaim` is the composite resource itself: an `apiextensions.crossplane.io/v2` `CompositeResourceDefinition` with `scope: Namespaced`. Its single Composition, `standard-postgres`, runs `function-patch-and-transform` and emits a `TenantDatabase` in the claim's namespace, named after the claim, with `engine: postgres`, `version: "16"` and `backupSchedule: "0 3 * * *"` as platform defaults, `storageGB` and `tier` copied through, and `tags.team` / `tags.costCenter` written as the labels `platform.internal/team` and `platform.internal/cost-center`. Crossplane receives RBAC for `tenantdatabases` through an aggregated ClusterRole in `gitops/compositions`.

Because the claim is a v2 composite, Crossplane's own fields live under `spec.crossplane` (for example `spec.crossplane.compositionRef.name`), not directly under `spec`. `API_REFERENCE.md` is updated to that shape.

## Consequences

- No provider, ProviderConfig, or intermediate cluster-scoped kind. The composed `TenantDatabase` carries a controller owner reference to its claim, so deleting the claim garbage-collects the database.
- `crossplane render` reproduces the composition offline from `gitops/compositions/examples`.
- Only the `standard` tier exists. A `custom` composition, when it arrives, is a second Composition selected by `spec.crossplane.compositionRef`; the admission policy currently rejects anything but `standard-postgres`.
- Any move to a cloud provider (RDS and the like) changes the composed resource, not the claim schema.
