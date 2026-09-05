# ADR-0013: The target Deployment is the canary; the controller owns the primary copy

**Status:** Accepted, 2026-09-05

## Context

The `CanaryRollout` schema in [`API_REFERENCE.md`](../API_REFERENCE.md#canaryrollout-crd) names a `targetDeployment` and nothing else about versions, so the CRD does not say what a "new version" is or where two versions live during a rollout. Three models were considered: the CR carries the candidate template; the controller clones a canary Deployment from a template change and reverts the target; or the target itself is the canary and the controller maintains a stable copy. Deployments are delivered through Git, and ArgoCD's selfHeal on stateless definitions ([ADR-0007](0007-argocd-bootstraps-the-platform-with-drift-policy-per-application.md)) would fight any controller that rewrote a user's pod template back to the old version.

## Decision

The user-facing Deployment always holds the desired version and serves as the canary. The controller owns:

- `<name>-primary`: a Deployment cloned from the target with its own selector, serving stable traffic at the replica count the target had before it was parked.
- `<name>-primary` and `<name>-canary`: ClusterIP Services with the target's ports (copied from the user's own Service `<name>`), selecting on the target's selector labels plus `platform.internal/canary-role`.
- The VirtualService of [ADR-0011](0011-istio-virtualservice-is-the-canary-traffic-router.md).

All carry a controller owner reference to the `CanaryRollout`, per the invariant that nothing is provisioned without one. The user's Deployment and Service are never owned, but the target's pod template gains the role label `canary` (its selector is immutable and stays untouched). A rollout starts when the hash of the target's pod template, with the controller's labels stripped, differs from the last hash the controller acted on. On success the accepted template is copied onto primary, primary rolls out, traffic returns to it and the target is scaled to zero. On rollback traffic returns to primary and the target is scaled to zero; the rolled-back hash is remembered so the same version is not retried. While idle the target has no pods.

## Consequences

- GitOps-compatible: Git describes the desired version; the controller only touches `spec.replicas` and one template label on the user's object. Under `kiln-tenants` (no selfHeal) the parked replica count shows as OutOfSync, which is intended.
- Rollback ground truth is physical: 100% of the VirtualService on primary, canary pods gone, primary pods still on the old template. The end-to-end test asserts exactly that.
- Users must own a Service named after the Deployment; the controller reports `ServiceMissing` otherwise.
- Editing `replicas` while idle is undone by the controller, which parks the target again; the new count is remembered for the next rollout. Reverting the target to the version primary already runs is recognised and does not start a rollout.
