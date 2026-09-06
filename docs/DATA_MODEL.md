# Data Model

The audit table is the only relational schema on the platform. Every other subsystem's state lives in its CRD ([`API_REFERENCE.md`](API_REFERENCE.md)). The table is owned by the Audit/RBAC service and reached only through Flyway migrations in `audit-service/src/main/resources/db/migration`.

## `audit_entry`

| Column | Type | Constraint | Meaning |
|---|---|---|---|
| `seq` | `bigint` | primary key, identity | Chain position. Assigned at insert; the chain is ordered by `seq`. |
| `event_id` | `uuid` | `unique` | The wire event's `eventId`. The unique constraint is the sole dedup mechanism: a redelivered record violates it and is acknowledged without a row. |
| `actor` | `text` | not null, indexed | |
| `action` | `text` | not null | |
| `resource` | `text` | not null, indexed | `<Kind>/<namespace>/<name>` |
| `occurred_at` | `timestamptz` | not null, indexed | The wire event's `timestamp`, microsecond precision. |
| `details` | `jsonb` | not null | The wire event's `details`, `{}` when absent. |
| `prev_hash` | `char(64)` | not null | The previous row's `hash`; 64 zeros for the first row. |
| `hash` | `char(64)` | not null | SHA-256 of this row's content and `prev_hash`, hex, lower case. |

Indexes: `(actor, occurred_at)`, `(resource, occurred_at)`, `(occurred_at)`. Time partitioning is deferred until volume warrants it ([`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md#6-audit-compliance-and-rbac-service)).

## Hash

```
hash = sha256( prev_hash + "\n" + event_id + "\n" + actor + "\n" + action + "\n" + resource
             + "\n" + occurred_at + "\n" + details )
```

where `occurred_at` is formatted `yyyy-MM-dd'T'HH:mm:ss.SSSSSS'Z'` in UTC and `details` is canonical JSON: object keys sorted lexicographically at every level, no whitespace, numbers as stored. The canonical form is recomputed from the stored `jsonb` on verification, so the hash depends on the content, never on the bytes a publisher happened to send.

## Write boundary

Two Postgres roles exist. `audit_writer` owns the table and is the only role with `INSERT` on it; its password lives in the Secret `audit-postgres` in namespace `kiln-audit`, mounted only by the audit service. `audit_reader` has `SELECT` only and exists for operators who need to inspect the table by hand. No other platform component holds a credential to this database ([ADR-0018](decisions/0018-the-audit-service-alone-computes-the-hash-chain.md)). The service never issues `UPDATE` or `DELETE`; a tampered row is detected by `GET /v1/audit/verify`, never repaired.

One known gap: the operator's ClusterRole grants cluster-wide Secret access because it creates each tenant's credentials in that tenant's namespace, and RBAC cannot carve one namespace out of a ClusterRole. The operator could therefore fetch `audit-postgres` through the API server even though nothing in its pod references it. The delivery controller, the chaos controller and the scheduler hold no Secret role at all. The NetworkPolicy `audit-postgres-writer-only` closes the operator's path on a CNI that enforces it; kind's default CNI does not, so the end-to-end test proves the RBAC facts and the pod specs, not the network.

## Insert protocol

One consumer instance processes `kiln.audit`. Each record is stored in one transaction that takes the advisory lock `pg_advisory_xact_lock(1)`, reads the last row's `hash`, computes this row's `hash`, and inserts. A unique-constraint violation on `event_id` rolls the transaction back and the record is acknowledged. The Kafka offset is committed only after the transaction commits, so a crash between the two redelivers the record and the constraint absorbs it.
