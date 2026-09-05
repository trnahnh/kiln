# Testing Strategy

General principle across every subsystem: business/decision logic is isolated from Kubernetes and network glue code so it's unit-testable on its own. Integration tests then prove the glue code actually works against a real cluster. Neither layer substitutes for the other.

## Operator

- **Unit tests:** the phase table lives in `operator/internal/lifecycle` with no Kubernetes dependency, and every documented transition, every rejected/conflicting transition, and the terminal `Failed` phase has a plain Go test there. The reconciler is tested with `envtest` (real API server, no controllers) in `operator/internal/controller`: provisioning and owner references, unsupported engine, idempotency, scaling, backup, restore, finalizer draining, and the scale-during-backup collision, which asserts on the PVC's actual spec and the Job's state rather than on `status.phase`. Run with `make test` in `operator/`.
- **Integration tests:** `TestTenantDatabaseLifecycle` in `operator/test/integration` (build tag `integration`) runs the manager in-process against the current kubeconfig context, a real `kind` cluster in CI. It provisions Postgres, loads rows through `psql` inside the pod, holds a backup Job open through the injected backup command, scales mid-backup, and asserts the PVC does not change until the Job completes, that the row count is unchanged afterwards, and that a restore from that dump brings back deleted rows. Run with `make test-integration`.

## Provisioning/GitOps

- **Unit tests:** Crossplane composition rendering tested with `crossplane render` against fixed input parameters, asserting the exact resource set produced.
- **Integration tests:** ArgoCD sync tested against a disposable preview cluster in CI; drift is introduced manually in the test and the expected detection/heal behavior is asserted per resource class.

## Scheduler plugin

- **Unit tests:** the scoring function is pure (no cluster I/O) and tested in isolation against synthetic cluster states, covering the cost/fragmentation/preemption-risk tradeoff explicitly, including edge cases (all nodes spot, no spot capacity available, latency-sensitive workload present).
- **Performance tests:** `scheduler_perf` benchmarks for placement latency under load, to confirm the custom scoring doesn't degrade scheduling throughput.

## Progressive delivery controller

- **Unit tests:** rollback decision logic tested against simulated Prometheus metric time series, three classes: healthy, degrading, noisy-but-healthy. Assert on the rollback/proceed decision and on false-positive rate across the noisy-but-healthy class.
- **Integration tests:** full canary run against a `kind` cluster with Istio/Linkerd installed, using a deliberately-broken deployment to confirm real traffic shifting and real rollback.

## Chaos/resilience module

- **Unit tests:** abort logic (SLO breach detection) tested independently of the fault-injection mechanism, using synthetic metric streams that cross the defined threshold.
- **Integration tests:** experiments run against a disposable namespace with synthetic load first. Blast-radius containment is tested explicitly: assert that a fault never propagates beyond the declared `labelSelector`/`maxReplicaPercentage` scope.

## Audit/RBAC service

- **Unit tests (JUnit/Mockito):** hash-chain verification logic, idempotency-key handling, and RBAC role checks, each tested independently of Kafka/Postgres.
- **Integration tests (Testcontainers):** real Kafka and Postgres instances spun up per test run. Redelivery is simulated by re-publishing the same event and asserting the unique constraint catches it rather than producing a duplicate row. Tampering is simulated by directly mutating a stored hash and asserting `/v1/audit/verify` detects the break.

## Cross-cutting

- CI runs the full unit test suite on every push; integration tests (anything requiring `kind`, Testcontainers, or a live Istio install) run on merge to main and nightly, given their longer runtime.
- Every subsystem's exit criterion in [`ROADMAP.md`](ROADMAP.md) is written to be provable by a specific test named in this document, not by manual click-through verification.
