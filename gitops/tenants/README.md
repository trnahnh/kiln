# Tenant requests

One directory per team, one `DatabaseClaim` per file, synced by the `kiln-tenants` ArgoCD
Application into the namespace each claim names. Nothing here is auto-healed or pruned:
a live change to a database request shows as OutOfSync and waits for a human.

Claim schema: [`docs/API_REFERENCE.md`](../../docs/API_REFERENCE.md#provisioning-claim-crossplane-composite-resource).
Org rules a claim must satisfy: [`gitops/policies`](../policies).
