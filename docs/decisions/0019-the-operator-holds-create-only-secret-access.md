# ADR-0019: The operator holds create-only Secret access; Crossplane's and ArgoCD's blanket grants are an accepted residual

**Status:** Accepted, 2026-09-06.

## Context

ADR-0018 makes the audit table's write boundary a single Postgres credential, the Secret `audit-postgres` in `kiln-audit`, held only by the audit service. The Phase 6 end-to-end test found that the operator's ServiceAccount could read that Secret: its generated ClusterRole granted every verb on Secrets cluster-wide, because the operator creates `<name>-credentials` in whichever namespace a `TenantDatabase` lives, and Kubernetes RBAC cannot carve one namespace out of a ClusterRole.

The first account of this conflated two different exposures. A NetworkPolicy restricts which pods may open a connection to the Postgres pod; it does nothing about what a ServiceAccount may read from the API server, and a Secret read is an API server call. The NetworkPolicy `audit-postgres-writer-only` stays, for the network exposure it does address. The API exposure needed its own answer.

The operator, it turns out, never reads a Secret. It creates the credentials once, treats AlreadyExists as success, does not watch or update the Secret, and the garbage collector deletes it through the owner reference. Its `get` was only a pre-check before `create`.

Two upstream identities can also read the Secret: the Crossplane core ServiceAccount, whose chart-installed ClusterRole grants every verb on Secrets so it can manage connection secrets, and the ArgoCD application controller, which the upstream install binds to cluster-admin. Narrowing either means a namespace-scoped ArgoCD install and a custom Crossplane RBAC, both upstream Helm changes with breakage risk.

## Decision

- The operator's RBAC grants `create` on Secrets and nothing else. The credentials Secret is created without a preceding read; AlreadyExists is success. The operator therefore cannot read any Secret in any namespace, `audit-postgres` included, structurally rather than by convention. The end-to-end test asserts both the `SubjectAccessReview` result and that the ClusterRole's Secret verbs are exactly `[create]`.
- The delivery controller, the chaos controller and agent, and the scheduler continue to hold no Secret role; the end-to-end test asserts that too.
- The Crossplane core ServiceAccount's and the ArgoCD application controller's ability to read `audit-postgres` is an accepted residual for the local platform. The closure is not RBAC surgery on upstream charts but removing the shared password: the audit service authenticating to Postgres with a workload identity or an external secret store. That is scheduled for no earlier than Phase 7 and gets its own ADR when it lands.
- The NetworkPolicy remains, documented for what it is: a network-level boundary on TCP 5432, enforced only on a CNI that implements NetworkPolicy, which kind's default does not.

## Consequences

- Least privilege for the operator costs nothing: no new component, no asynchronous RBAC generation, no change to how a `TenantDatabase` provisions.
- A compromised operator could still create Secrets anywhere, but not read, list or overwrite one.
- Anyone reading the write boundary must read it as two statements: no kiln controller can read the credential; two upstream platform components can, and are named. `DATA_MODEL.md` carries the current form of that statement.
