# Roadmap

Sequenced by dependency, not by calendar time. Each phase has a hard exit criterion. Don't start the next phase until the current one's criterion is genuinely met, not approximately met.

- [x] **Phase 0: Foundation**
  Scope: local `kind` cluster, base repo structure, CI skeleton, Prometheus/Grafana stood up.
  Exit criterion: empty cluster with observability running, CI pipeline green on an empty test.

- [x] **Phase 1: Operator**
  Scope: `TenantDatabase` CRD, reconciler, backup/restore logic.
  Exit criterion: can provision, scale, back up, and restore a Postgres instance through the CRD alone; concurrent scale+backup test passes with zero data loss.

- [x] **Phase 2: Policy + GitOps**
  Scope: Crossplane compositions, ArgoCD sync, OPA/Kyverno gating in front of the operator.
  Exit criterion: a request that violates policy is rejected before reaching the operator; a valid request flows end to end through Git.

- [x] **Phase 3: Scheduler plugin**
  Scope: scoring function, spot-price integration.
  Exit criterion: demonstrable cost reduction on a synthetic multi-node cluster versus the default scheduler, with the scoring rationale documented in code and in `SYSTEM_DESIGN.md`.

- [x] **Phase 4: Progressive delivery**
  Scope: canary controller, statistical rollback logic.
  Exit criterion: injected synthetic regression triggers auto-rollback; injected noise does not false-trigger.

- [x] **Phase 5: Chaos module**
  Scope: fault injection, SLO scoring, abort logic.
  Exit criterion: an experiment against a test service produces a resilience score; a forced SLO breach triggers auto-abort within the defined threshold.

- [x] **Phase 6: Audit/RBAC service**
  Scope: Spring Boot service, Kafka event stream, hash-chained log, RBAC enforcement. Also delivered here, pulled forward from Phase 7: every subsystem publishes to the audit stream, and the operator's in-cluster deployment through ArgoCD, because the exit criterion needs Phase 1 actions on the e2e cluster.
  Exit criterion: every action from Phases 1-5 is visible in the audit log; a tampered entry is detected on verification; a duplicate Kafka delivery does not duplicate an entry.

- [x] **Phase 7: Integration + validation**
  Scope: the end-to-end request flow through the REST path across the six already-deployed subsystems, with one OpenTelemetry trace spanning it (the cross-cutting section of [`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md#cross-cutting-observability)); run the Validation Plan in [`METRICS.md`](METRICS.md#validation-plan).
  Exit criterion: the full synthetic case study completes and produces a before/after comparison table against the baselines in `METRICS.md`.

- [ ] **Phase 8: Durable audit outbox**
  Scope: every controller records an audit event in its CR's status in the same write that commits the transition, and a drainer publishes it afterwards, so a crash between the transition and Kafka's acknowledgement loses nothing (ADR-0022, superseding the memory-only buffer of ADR-0017). The REST path publishes before it applies. The scheduler's `SCHEDULE` event stays memory-buffered as a named residual.
  Exit criterion: with every controller SIGKILLed repeatedly during a burst of requests, each phase transition observed by an informer the test owns has exactly one audit row, the chain verifies, and `kiln_audit_publish_failures_total` is zero.

- [ ] **Phase 9: External anchor for the hash chain**
  Scope: the audit service periodically signs the chain head (Ed25519), chains each checkpoint to the previous one, and writes it to object storage under a compliance-mode object lock (MinIO on kind, S3 in cloud); `GET /v1/audit/verify` checks every checkpoint and fails closed when the store is unreachable; an offline verifier needs only the public key, the database and the bucket. Defends against a database writer who rewrites rows and recomputes every hash; a compromised service or Kafka is out of scope and stated so (ADR-0023).
  Exit criterion: a rewrite of the whole chain from genesis with every hash recomputed is named by `/v1/audit/verify` and by the offline verifier against the checkpoints; the bucket owner cannot delete a checkpoint; the unanchored window never exceeds 100 rows or 60 seconds.

## Notes

- No subsystem starts before the prior one's exit criterion is met. This is the single biggest scope-creep risk on a six-subsystem platform, treat the checklist above as a hard gate, not a suggestion.
- Every phase's exit criterion should be provable by a test in CI, not by manual verification, wherever that's possible. See [`TESTING.md`](TESTING.md) for the testing approach per subsystem.
