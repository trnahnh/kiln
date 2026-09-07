# ADR-0020: The audit network boundary is inert on kind, and the accepted residual is conditional on an enforcing CNI

**Status:** Accepted, 2026-09-06. Supersedes the NetworkPolicy framing of [ADR-0019](0019-the-operator-holds-create-only-secret-access.md); everything else in ADR-0019 stands.

## Context

ADR-0019 narrowed the operator's Secret grant to `create` and accepted, as a residual, that the Crossplane core ServiceAccount and the ArgoCD application controller can still read the audit writer credential `audit-postgres` through the API server. It kept the NetworkPolicy `audit-postgres-writer-only` and described it as "enforced only on a CNI that implements NetworkPolicy, which kind's default does not", without saying what follows from that for the residual.

What follows matters. A NetworkPolicy is the only thing standing between a component that can read the password and a component that can write the table. Where the policy is not enforced, reading the password is writing the table.

## Decision

On kind, therefore, the NetworkPolicy `audit-postgres-writer-only` is inert: kindnet does not enforce NetworkPolicy, so it neither blocks nor logs a connection to the Postgres pod from any other pod. That makes the accepted residual conditional. On the local kind cluster, Crossplane core and the ArgoCD application controller can both read the writer password through the API server and connect to the database over the network, so for them the write boundary is open at both layers, and this is accepted only because that cluster is a development and CI environment whose audit trail is discarded with it. On any cluster meant to carry a real audit trail, the acceptance holds only if the CNI enforces NetworkPolicy, so that a read of the password does not become a write to the table; without an enforcing CNI the residual is not accepted, and the shared password must be removed (workload identity or an external secret store) before that cluster is trusted with the trail.

## Consequences

- The Phase 6 exit criterion is unaffected: it proves the chain, the dedup and the RBAC facts, none of which depend on the network boundary.
- Any move of the platform off kind, in Phase 7 or after, must first name the CNI and whether it enforces NetworkPolicy; that answer decides whether this residual still stands or the shared password has to go first.
- [`DATA_MODEL.md`](../DATA_MODEL.md) links here for the current statement of the write boundary alongside ADR-0019.
