# ADR-0018: The audit service alone computes the hash chain; publishers send unchained events

**Status:** Accepted, 2026-09-06.

## Context

The event schema as first written had every subsystem emit `prevHash` and `hash`. A chain across many independent publishers cannot be built that way: no publisher knows what the previous entry is, and two publishers writing at once would fork it. The CLAUDE.md invariant already names the Audit/RBAC service as the sole holder of write credentials to the audit table, and the hash chain and the `eventId` uniqueness constraint as the only tamper-evidence and dedup mechanisms.

The actor is a second gap. A controller reconciling a CR does not know who applied it, and Kubernetes Events carry no identity either.

## Decision

- Publishers send a **wire event** with `eventId`, `actor`, `action`, `resource`, `timestamp` and `details`, and nothing about the chain. The service computes `prevHash` and `hash` at insert, under a Postgres advisory lock, in `seq` order ([`DATA_MODEL.md`](../DATA_MODEL.md)). The `details` object is part of the hashed content, canonicalised from the stored value, so the hash depends on content rather than on a publisher's byte layout.
- The **write boundary is structural**: the only Postgres role with `INSERT` on `audit_entry` is `audit_writer`; its credential is one Secret in the service's namespace, mounted only by the service. Publishers never hold a database credential of any kind. The service's own REST path publishes to Kafka and stores its events only when it consumes them, so even the service does not bypass the stream.
- **Actor attribution**: the REST path stamps `platform.internal/requested-by: <JWT subject>` on every CR it applies, and the standard composition copies it from a `DatabaseClaim` to its `TenantDatabase`. Controllers copy the annotation into `actor` and fall back to `system:<controller>` when it is absent, so a CR applied directly with `kubectl` or through the GitOps path is attributed to the platform rather than rejected.
- The action vocabulary grows so every Phase 1 to 5 action is expressible: `PROVISION`, `BACKUP`, `RESTORE`, `SCALE`, `SCHEDULE`, `DEPLOY`, `ROLLBACK`, `CHAOS_EXPERIMENT`, alongside the REST path's `PROVISION_REQUEST` and `POLICY_DENY`. `details.outcome` names the result.

## Consequences

- `API_REFERENCE.md`'s event schema is corrected to distinguish the wire event from the stored entry; the earlier shape was never implemented and nothing depended on it.
- Chain order is the service's insert order, not the publishers' clocks. Two events from different subsystems about the same instant may be stored in either order; `timestamp` records the publisher's view and `seq` the chain's.
- `POLICY_DENY` can only be captured on the REST path, where the caller is known and the admission rejection is observed. A denial of a direct `kubectl` apply creates no object and no event, and is not audited.
- Requiring the actor annotation at admission would make attribution complete but would reject every existing GitOps-delivered claim; that is a Phase 7 question if the validation plan needs it.
