# ADR-0007: ArgoCD bootstraps the platform from Git, with drift policy set per Application class

**Status:** Accepted, 2026-09-05

## Context

Phase 2's exit criterion requires a valid request to flow end to end through Git with no manual `kubectl apply`. [`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#2-provisioning-and-gitops-layer) sets the drift rule: auto-heal anything stateless, flag-and-alert anything stateful. Crossplane and Kyverno are distributed as Helm charts; ADR-0001 chose flat manifests for observability and Helm is not installed locally. Alternatives were vendoring rendered chart output into the repo, or installing components by hand with Helm.

## Decision

ArgoCD is the one component installed outside Git: `gitops/argocd/install` is a kustomization pinning the upstream install manifest, applied once with `kubectl apply -k`. Everything else is an ArgoCD `Application` under `gitops/argocd/apps`, reached through the root app-of-apps `gitops/argocd/root.yaml`:

| Application | Source | Drift policy |
|---|---|---|
| `crossplane`, `kyverno` | Helm charts at pinned versions, rendered by ArgoCD | selfHeal, prune |
| `kiln-platform` | `gitops/compositions` and `gitops/policies` in this repo | selfHeal, prune |
| `kiln-tenants` | `gitops/tenants` in this repo, recursive | no selfHeal, no prune |

`kiln-tenants` holds `DatabaseClaim`s, which describe databases and are therefore stateful: a live edit shows as OutOfSync and is never reverted. Everything in `kiln-platform` is stateless configuration and is healed. The `kyverno` Application ignores `metadata.labels`, `metadata.annotations` and `spec.conversion` on CRDs, which the chart renders empty and the API server normalises.

## Consequences

- No Helm binary anywhere in the toolchain; chart versions are pinned in the Application specs and upgraded by a Git change.
- Bootstrap order is handled by ArgoCD retries and sync waves, not by scripts: charts first, then Function, XRD, Composition and policies, then tenant claims.
- Tenant requests reach the cluster only through Git; the e2e test proves it with a throwaway branch and temporary Applications, and proves both drift behaviours.
- Weakening a policy in the cluster (for example switching `validationActions` to Audit) is reverted by selfHeal, so the enforcement boundary of ADR-0006 is itself Git-owned.
- ArgoCD reads the public repository anonymously. A private repository would add a repository credential Secret to the bootstrap, superseding this ADR's install step.
