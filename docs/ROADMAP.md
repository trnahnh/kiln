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
  Scope: Spring Boot service, Kafka event stream, hash-chained log, RBAC enforcement.
  Exit criterion: every action from Phases 1-5 is visible in the audit log; a tampered entry is detected on verification; a duplicate Kafka delivery does not duplicate an entry.

- [ ] **Phase 7: Integration + validation**
  Scope: wire all six subsystems into one coherent request flow; run the Validation Plan in [`METRICS.md`](METRICS.md#validation-plan).
  Exit criterion: the full synthetic case study completes and produces a before/after comparison table against the baselines in `METRICS.md`.

## Notes

- No subsystem starts before the prior one's exit criterion is met. This is the single biggest scope-creep risk on a six-subsystem platform, treat the checklist above as a hard gate, not a suggestion.
- Every phase's exit criterion should be provable by a test in CI, not by manual verification, wherever that's possible. See [`TESTING.md`](TESTING.md) for the testing approach per subsystem.
