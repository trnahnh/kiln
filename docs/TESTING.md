# Testing Strategy

General principle across every subsystem: business/decision logic is isolated from Kubernetes and network glue code so it's unit-testable on its own. Integration tests then prove the glue code actually works against a real cluster. Neither layer substitutes for the other.

## Operator

- **Unit tests:** the phase table lives in `operator/internal/lifecycle` with no Kubernetes dependency, and every documented transition, every rejected/conflicting transition, and the terminal `Failed` phase has a plain Go test there. The reconciler is tested with `envtest` (real API server, no controllers) in `operator/internal/controller`: provisioning and owner references, unsupported engine, idempotency, scaling, backup, restore, finalizer draining, and the scale-during-backup collision, which asserts on the PVC's actual spec and the Job's state rather than on `status.phase`. Run with `make test` in `operator/`.
- **Integration tests:** `TestTenantDatabaseLifecycle` in `operator/test/integration` (build tag `integration`) runs the manager in-process against the current kubeconfig context, a real `kind` cluster in CI. It provisions Postgres, loads rows through `psql` inside the pod, holds a backup Job open through the injected backup command, scales mid-backup, and asserts the PVC does not change until the Job completes, that the row count is unchanged afterwards, and that a restore from that dump brings back deleted rows. Run with `make test-integration`; CI runs it on the same five-node `kind-config.yaml` as every other job. On failure it dumps Job logs, the database log, readiness transitions, cluster-wide warning events, node conditions and kube-proxy/kindnet logs; that dump is how the Service-selector bug (Job pods selected as database endpoints) was found in CI.

## Provisioning/GitOps

- **Unit tests:** Crossplane composition rendering tested with `crossplane render` against `gitops/compositions/examples/databaseclaim.yaml`, asserting the composed `TenantDatabase` carries the platform defaults, the requested storage, and the tag labels (CI job `gitops-unit`; locally the CLI needs Linux and Docker, so run it in WSL). The admission rules are tested offline with `kyverno test gitops/policies/tests`: one good and one at-ceiling claim pass, every violation class fails, and the direct `TenantDatabase` ceiling is covered both ways.
- **Integration tests:** `TestPhase2PolicyAndGitOps` in the `e2e/` module (build tag `e2e`) runs against a cluster bootstrapped from `gitops/argocd` (CI job `platform-e2e`). It reads the Kyverno webhook's `failurePolicy` from the cluster, applies violating claims and a violating `TenantDatabase` directly and asserts nothing exists afterwards, then pushes a throwaway branch `e2e/<run>` of this repository holding one valid and one violating claim and points temporary ArgoCD Applications at it: the valid claim must become a `TenantDatabase` with the composed spec, the violating one must fail the sync at admission with no object created. Drift is then introduced live: a claim edit must show OutOfSync and stay unreverted (stateful class), and weakening a policy's `validationActions` must be reverted by selfHeal (stateless class). The branch and Applications are removed at the end.

## Scheduler plugin

- **Unit tests:** the scoring function is pure (no cluster I/O) in `scheduler-plugin/internal/scoring` and tested in isolation against synthetic cluster states, covering the cost/fragmentation/preemption-risk tradeoff explicitly, including the edge cases: all nodes spot (a latency-sensitive pod is unschedulable rather than placed), no spot capacity (equal costs leave fragmentation to decide), latency-sensitive workload present (spot nodes filtered before scoring), the cheap-spot-wins-even-at-maximal-risk case that pins the weighting, GPU stranding, and score bounds. `internal/pricing` covers the node-label contract and the AWS source against fakes; `internal/plugin` covers the framework adapter (Filter, PreScore, Score, args). Run with `make test` in `scheduler-plugin/`.
- **Performance tests:** upstream `scheduler_perf` is not importable, so `scheduler-plugin/test/perf` (build tag `perf`, `make bench`) reproduces its method: a real kube-apiserver from envtest, the real scheduler in-process with a default profile and the kiln profile side by side, 200 synthetic nodes, a burst of 500 pods per profile, and creation-to-bind latency measured through a watch. It reports p50, p99 and pods/s for both profiles so any degradation from the custom scoring is a number, alongside `BenchmarkScore` for the pure function at 100/1000/5000 nodes.
- **Exit-criterion replay:** `TestPhase3SchedulerCostReduction` in the `e2e/` module (build tag `e2e`, CI job `platform-e2e`) builds one seeded trace of 60 pods (20% latency-sensitive, 40% standard, 40% batch, three sizes, 30 to 60 second sleeps, two-second arrivals) and replays it first through `default-scheduler` and then through `kiln-scheduler` on the same five-node kind cluster. Each run is billed from the API server's record alone: every pod's node and its container's actual start and finish times, merged per node, so a node is charged its hourly-cost label for every second it hosts at least one trace pod (node-occupancy instance-hours, `METRICS.md`). The test asserts kiln-scheduler bills strictly less than the default and that no latency-sensitive pod ran on a spot node, and logs the percentage. The plugin's own score is never read.
- **Manual validation, outside CI:** the AWS price source is tested against fakes. To confirm the fakes still mirror the real APIs, run once locally with AWS credentials in the standard credential chain (a few read-only `DescribeSpotPriceHistory` calls and one public S3 fetch, trivial cost):

  ```
  cd scheduler-plugin && go test -tags=liveaws ./internal/pricing/aws/ -run Live -v
  ```

  It is excluded from every default `go test` invocation, never runs in CI, and is not part of Phase 3's exit criterion.

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
