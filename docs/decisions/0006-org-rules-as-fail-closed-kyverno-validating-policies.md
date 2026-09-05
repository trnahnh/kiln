# ADR-0006: Org rules are fail-closed Kyverno ValidatingPolicies on both the claim and the TenantDatabase

**Status:** Accepted, 2026-09-05

## Context

CLAUDE.md's invariant: policy evaluation happens before provisioning, enforced structurally by admission webhook ordering, never by an application-level flag. [`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#2-provisioning-and-gitops-layer) names OPA/Kyverno as the policy layer and keeps it separate from RBAC (who may submit) in the Audit/RBAC service. Phase 2 needs two declared org rules: mandatory tagging and a storage ceiling. Alternatives were classic pattern-based Kyverno `ClusterPolicy`, OPA Gatekeeper constraints, or the Kubernetes-native `ValidatingAdmissionPolicy`.

## Decision

Rules are `policies.kyverno.io/v1` `ValidatingPolicy` objects (CEL) with `failurePolicy: Fail` and `validationActions: [Deny]`:

- `databaseclaim-org-rules` on `DatabaseClaim`: `spec.parameters.tags.team` and `spec.parameters.tags.costCenter` present and non-empty; `spec.parameters.storageGB <= 100`; `tier` is `standard`; `compositionRef`, if set, is `standard-postgres`.
- `tenantdatabase-storage-ceiling` on `TenantDatabase`: `spec.storageGB <= 100`.

Every denial message starts with `POLICY_DENIED rule=<name>` so the error code in `API_REFERENCE.md` and the failing rule are visible to the developer and to ArgoCD's sync result.

## Consequences

- A violating claim is rejected by the API server before the object exists, so Crossplane never composes and the operator never sees a `TenantDatabase`. A violating `TenantDatabase` applied directly is rejected the same way. Whether someone may apply a `TenantDatabase` directly at all is Phase 6 RBAC and is not duplicated here.
- Fail-closed: if Kyverno is unavailable, requests are refused, not admitted unchecked. The e2e test reads the webhook's `failurePolicy` from the cluster.
- Rules are testable offline with `kyverno test` and evaluated in-cluster by the same engine.
- Changing the ceiling or the mandatory tags is a policy edit under `gitops/policies`, reviewed through Git and auto-healed by ArgoCD if changed live.
