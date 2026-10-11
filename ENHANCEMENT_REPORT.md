# Enhancement report, 2026-10-10

Branch `enhance/evidence-depth`, nine commits on top of `main` at `cc6c5f0`. Nothing pushed, nothing on `main`, nothing deleted, nothing outside the repository touched. No cluster was available (Docker off on this machine), so every new number here comes from code that runs on a laptop; the one cluster-side change (the Phase 8 harness fix) is vetted but unverified until a `main` run.

## 1. Audit

### Architecture

Six subsystems behind one request path: a Kyverno fail-closed admission gate, a Crossplane/ArgoCD provisioning path, a `TenantDatabase` operator, a cost-aware scheduler plugin, a canary controller with a sequential test, a chaos controller with a node agent, and a Java audit service that is the sole writer of a hash-chained table fed by Kafka. The structure matches the docs; the invariants in `CLAUDE.md` are enforced where they say (filter before score, admission ordering, owner references, single writer). Two gaps between the pitch and the code:

- **"Six subsystems behind one fail-closed policy gate" overstates it.** The admission policies in `gitops/policies` cover three kinds: `DatabaseClaim`, `TenantDatabase` and `ChaosExperiment`. `CanaryRollout` has no policy, and the scheduler and delivery controller are not "behind" the gate in any sense. Accurate: one REST entry point (`POST /v1/requests`) for three resource kinds, with a fail-closed admission gate on the provisioning and chaos kinds.
- **Phase 8's end-to-end proof has never passed.** See section 2.

### Every claimed number

| Claim (as pitched) | Where it comes from | Reproduce | Reproducible? | How strong the evidence is |
|---|---|---|---|---|
| 45.5% cluster spend cut | `TestPhase3SchedulerCostReduction` log in CI run 34085396203 | `go test -tags=e2e -run TestPhase3 ./e2e/` on the bootstrapped kind cluster | Yes in CI; not here (no Docker) | **Weak as a cost claim, sound as a mechanism proof.** One run, n=1, 60 sleeping pods on an empty four-worker kind cluster with price labels the repo itself assigned (spot 0.03, on-demand 0.10). The billing rule charges a node only while it hosts a trace pod, so the number mostly measures that the plugin packs onto two cheap nodes while the default spreads over four. The public-trace replay below shows the same rule gives 91.8% on an empty cluster and 0.0% on a full one. The real result is structural: zero latency-class pods on spot versus four for the default. |
| zero SLA violations | same test, `violations` counter | same | Yes | Strong: the class filter runs before scoring, so it cannot be outweighed; the kind run and the public-trace replay (0 of 521 production-priority tasks on spot, against 249 to 263 for the default model) agree. "SLA" is the wrong word, though: nothing measures a latency SLA; what is zero is the count of latency-class placements on reclaimable capacity. |
| caught 1,000 of 1,000 seeded regressions | `TestSimulatedDegradingCanariesAreRolledBack`, four classes of 1,000 seeded rollouts each | `cd delivery-controller && go test ./internal/analysis -run Simulated -v` | Yes, reproduced here to the digit in 0.7 s | **Moderate.** It is 4,000 of 4,000, so the pitch undersells the count, but every regression is drawn from the author's own traffic model: independent requests at 200 rps, Bernoulli failures, four regression shapes (step to 30%, step to 2%, ramp, latency tail). On a real cluster the controller has caught exactly one regression, a flagrant 30% one, in 50 s, and promoted 29 healthy deploys with no false rollback. The model-based 100% says the decision rule has no blind spot inside its own assumptions; it says nothing about correlated failures, retries or diurnal traffic. |
| 2.8% false rollback rate | the bursty class of `TestSimulatedNoisyHealthyCanariesStayUnderTheFalseRollbackCeiling` | same | Yes | Moderate: 2.8% is the worst of four noisy-but-healthy classes (the others are 0.1%, 0.4%, 0.0%), under the 5% alpha. Say "worst case 2.8% across the noisy classes, 0 of 29 on the cluster". |
| Ready in 11 s p50 | Phase 7 `validation-results` artifact of run 34085396203 | `go test -tags=e2e -run TestPhase7Validation ./e2e/` | Yes in CI | Strong for what it is: ten claims, latency from audit rows cross-checked against pod Ready and the trace within 5 s. The baseline ("hours to a day") is narrative, labelled as such. kind, not a cloud. |
| 6 subsystems, one fail-closed gate | `gitops/policies`, `README.md` | `kyverno test gitops/policies/tests` | Yes | See above: three kinds gated. |

What the docs claim that is not true today: `TESTING.md` and `ROADMAP.md` describe the Phase 8 end-to-end proof as running in CI. It has failed on every run since it was pushed.

## 2. Found during the audit: the nightly was red for 29 days

`gh run list` showed every `ci` run since the Phase 8 push on 2026-09-12 failing in `platform-e2e`, 29 runs through 2026-10-10, ten other jobs green each time. The log names the line: `TestPhase8Outbox` requires every controller to have been killed without grace at least three times, got two, because the burst it drives finishes in 137 to 146 s and the 40-second alternating cadence delivers only two grace-0 kills by then. The assertion fired before the actual proof subtest ran. No notification exists for a scheduled run's failure, and the push run was not watched.

- Fix: `d4967a1`, the killer keeps going after the burst until the minimums hold, bounded at six cadences. Vetted with the `e2e` tag; CI runs only on `main` and nightly, so **unverified** on this branch.
- Detection: `200248f`, a `nightly-alert` job that opens or comments on a GitHub issue when a scheduled run fails. YAML validated; unverified until a nightly runs it.
- Account: `docs/postmortems/2026-10-10-nightly-red-for-29-days.md`, blameless, with the timeline, root cause, contributing conditions and five action items.

## 3. Ranking of the candidates

Effort is what each took or would take on this machine. Keywords are the general terms a host searching by concept would match, not product names.

| # | Candidate | Hours | Keywords earned | Done? |
|---|---|---|---|---|
| 1 | Blameless postmortem of a real failure (the red nightly) plus its fix and alert | 1.5 | incident postmortem, root cause analysis, CI reliability, alerting gap, test harness design | Yes |
| 2 | Canary detector versus a multi-window burn-rate alert and a consecutive-breach threshold at equal false-rollback rate | 2.5 | SLO-based alerting, error budget burn rate, sequential hypothesis testing, false-positive control, time to detect, progressive delivery | Yes |
| 3 | Scheduler scoring on a public workload (Google 2011 cluster trace) in a trace-driven simulator, with throughput at 12.5k machines | 3.5 | cluster scheduling, bin packing, trace-driven simulation, preemptible capacity, scheduler throughput, workload traces | Yes |
| 4 | SLOs and error budgets for the platform itself | 0.75 | service level objectives, error budgets, service level indicators | Yes, folded into `METRICS.md` |
| 5 | Scalability limits of the controllers | 0.25 done, 4 for the rest | scalability, benchmarking | Partly: scoring micro-benchmark rerun (linear, 87 ns per node, zero allocations, 0.44 ms per pod at 5,000 nodes) and the trace replay's placements per second; the envtest scheduler benchmark and any reconciler-fan-out test need a cluster or envtest binaries not available on Windows |
| 6 | KWOK or a scheduler simulator with the real `kube-scheduler` binary | 4 | same as 3 | No: needs Docker or Linux envtest binaries; the pure-model replay was the honest substitute and is labelled as a model, not the binary |

## 4. Results

### 4.1 Detector comparison (`delivery-controller/internal/analysis/comparison_test.go`)

**Before:** the sequential test's false-rollback and miss rates were known only against itself. **After:** the same traffic models and seeds drive a consecutive-breach threshold (the Flagger / Argo Rollouts shape) and a multi-window multi-burn-rate alert (the SRE Workbook shape), each swept over its tuning grid and reported at the fastest point under the same 5% false-rollback and 10% miss ceilings. The sequential test runs untuned at its defaults; the alternatives are tuned in-sample on these classes, which favours them.

| Detector | Worst false rollback | Step to 30%, detect p50 | Step to 2x limit, detect p50 / missed | Ramp, detect p50 | Latency tail, detect p50 |
|---|---|---|---|---|---|
| Sequential test + CUSUM (this controller) | 2.8% | 30 s | 1m15s / 0.0% | 1m45s | 30 s |
| Threshold, limit x1.5, 4 consecutive | 2.0% | 45 s | 1m15s / 6.6% | 1m45s | 45 s |
| Multi-window burn rate, x1.25 | 1.8% | 15 s | 3m0s / 2.0% | 3m0s | 3m0s |

Mixed, and recorded as such: the burn-rate alert wins on flagrant regressions (one window), the sequential test is the only rule with no misses on subtle ones, the threshold rule sits between. Full table, the sweep's trade-off curve and what the model cannot show are in `docs/METRICS.md`, "Detector comparison".

Reproduce:

```bash
cd delivery-controller && KILN_COMPARISON_REPORT=/tmp/comparison.md KILN_COMPARISON_SWEEP=1 go test ./internal/analysis -run TestDetectorComparison -v -count=1
```

Keywords: SLO-based alerting, error budget burn rate, sequential hypothesis testing, false-positive control, time to detect, progressive delivery, canary analysis.

Story: a release system that rolls back on a single bad window pages people for noise, and one that waits for a long window ships a slow regression to everyone; at Google scale both costs are paid thousands of times a day. The comparison shows, at a fixed false-rollback budget, exactly which regressions each rule catches and how fast, so the choice of rule becomes an SLO decision instead of a default. The same trade-off governs burn-rate paging for any service with an error budget.

### 4.2 Public-trace replay (`scheduler-plugin/test/trace/trace_test.go`, `hack/google-trace.sh`, `make trace`)

**Before:** the scheduler's saving was one kind run on 60 pods. **After:** the first 93.5 minutes of the public Google 2011 cluster trace, 12,477 machines and 57,012 placements (521 production-priority, 42,626 standard, 13,865 free-tier), placed by the plugin's own scoring function with the scheduler's 5% candidate sampling, by a least-allocated-plus-balanced model of the default profile, and by the machine the trace itself recorded, under synthetic spot labels on half the machines (the trace carries no prices).

| Scenario | Placement | Billed $ | vs default | Machines touched | Latency-class on spot | Unplaced | Placements/s |
|---|---|---|---|---|---|---|---|
| Empty cluster | default model | 723.06 | | 12,403 | 249 | 0 | 74,410 |
| Empty cluster | kiln scoring | 59.56 | -91.8% | 2,104 | 0 | 0 | 11,466 |
| Empty cluster | trace's own (Borg) | 541.87 | -25.1% | 10,340 | 253 | 63 | n/a |
| Carrying the trace's 132,982 running tasks | default model | 1,329.75 | | 2,499 | 263 | 24,305 | 326 |
| Carrying the trace's running tasks | kiln scoring | 1,329.75 | -0.0% | 2,638 | 0 | 24,362 | 280 |
| Carrying the trace's running tasks | trace's own (Borg) | 1,329.60 | -0.0% | 10,340 | 253 | 63 | n/a |

Two honest findings. On an empty cluster the node-occupancy rule hands the packing strategy a 91.8% "saving", the same artefact the kind number carries, because it charges a machine only while it hosts a task; that is an upper bound for a fleet where consolidation can release every idle machine. On the cluster as the trace actually found it, already 92% requested, nothing can be released and the bill is identical to the cent: placement alone saves nothing when no machine ever empties. What holds in both scenarios is the structural guarantee: zero production-priority tasks on reclaimable capacity, against 249 to 263 for an economics-blind placement under the same labels. At spot fractions 0.25 and 0.75 the empty-cluster figure is -94.1% and -88.2%, the full-cluster figure stays -0.0%, and the latency-class count stays 0 against 138 and 384 for the default model; the full sensitivity table and the reading are in `docs/METRICS.md`, "Public-trace replay".

Scalability: 11,466 placements per second through the kiln scoring on the empty 12.5k-machine cluster (74,410 for the simpler default model) and about 300 per second for both when almost nothing fits and the candidate scan walks the whole cluster; the pure score is 87 ns per node with no allocations.

Reproduce:

```bash
bash hack/google-trace.sh            # 4.5 MB, checksum-verified against the bucket's SHA256SUM
make -C scheduler-plugin trace       # KILN_TRACE_SPOT_FRACTION=0.25|0.5|0.75, KILN_TRACE_SEED
```

Keywords: cluster scheduling, bin packing, trace-driven simulation, preemptible capacity, scheduler throughput, workload traces, capacity planning.

Story: Borg placed these tasks on a cluster that was already full, and the replay shows that on such a cluster a smarter placement score moves nothing, while on a cluster with room it moves almost everything; the money is in how much capacity the autoscaler can hand back, and the scheduler's job is to make that possible without ever putting a production task on capacity that can vanish. That is the same split Google draws between the scheduler and the fleet manager. The replay turns a one-cluster anecdote into a bounded claim on a workload with real shapes, priorities and arrival times.

### 4.3 Postmortem and the harness fix

**Before:** 29 red nightlies, no alert, Phase 8 unproven, docs describing a proof that never ran. **After:** root-caused and fixed on the branch, an alert job for the next time, a postmortem with action items, and the README and session log say plainly that the Phase 8 end-to-end proof is still pending.

Reproduce: `gh run list --workflow ci.yaml --limit 40` shows the streak; `gh run view 38045166324 --log-failed | grep phase8_test.go:275` shows the assertion.

Keywords: incident postmortem, root cause analysis, CI reliability, alerting gap, test harness design, blameless culture.

Story: a proof that never runs is indistinguishable from one that passes if nobody looks, and at scale nobody can look at everything; the only defence is making a failure an event, not a colour. The fix changes the test to wait for its own precondition instead of asserting it, and the alert makes a red nightly open an issue. The postmortem records why a careful project still shipped a month of silent red.

### 4.4 Service level objectives (`docs/METRICS.md`, "Service level objectives")

Seven objectives derived from the validation evidence, each with its indicator's source (audit rows, histogram, node state), a 28-day window and budget or an explicit "no budget, invariant", and what the evidence shows so far. None has yet been monitored over a real window; the table says so.

Keywords: service level objectives, service level indicators, error budgets, error budget policy.

Story: an exit criterion is cleared once; an SLO is kept every day, and the budget is what lets a team ship without a meeting. Writing the objectives against the audit rows means the same query that produced the validation table measures compliance in production. The budget policy, stop feature work when the budget is spent and write it up, is the one every large SRE organisation runs.

## 5. Keywords earned overall

cluster scheduling, bin packing, trace-driven simulation, progressive delivery, sequential hypothesis testing, SLO-based alerting, error budget burn rate, incident postmortem.

## 6. A resume phrasing that the evidence supports

- Cost-aware cluster scheduler plugin: a workload-class filter that kept every latency-sensitive placement off preemptible capacity on both a kind replay and 57,012 placements from Google's public cluster trace; node-occupancy saving of 45.5% on the kind replay, bounded between 0% and 92% on the trace depending on how full the cluster is.
- Progressive delivery with a sequential test and CUSUM: 4,000 of 4,000 seeded regressions rolled back, worst-case 2.8% false rollback on noisy traffic; at equal false-rollback cost, the only rule of three with no misses on 2x regressions, versus a burn-rate alert and a consecutive-breach threshold.
- Self-service provisioning with a fail-closed admission gate on the provisioning and chaos kinds: standard Postgres Ready in 11 s p50, 16 s p95, with every latency cross-checked against cluster state and an audit row.

## 7. What is not finished

- The Phase 8 fix and the nightly alert are unverified until merged to `main` and run. Watch the first nightly after the merge; the subtest "every controller comes back and every outbox drains" has never executed in CI.
- The trace replay is a model of the scoring, not the scheduler binary, and its prices are synthetic. Running the real binary on the trace (KWOK or envtest) needs Linux or Docker.
- The pitch's "six subsystems behind one policy gate" should be reworded as in section 1.

## 8. Commits

```
d4967a1 fix(e2e): keep killing controllers until the outbox proof's kill minimums hold
aa5ef9e test(delivery): compare the sequential test with threshold and burn-rate detectors at equal false-rollback cost
f69d851 test(delivery): count a rollback before a regression's onset as premature, not as a detection
d662a15 test(scheduler): replay the Google 2011 cluster trace through the placement model
200248f ci: open an issue when the nightly is red
cd16a58 docs: postmortem for the 29-day red nightly
cc9f90d docs: SLOs, the detector comparison and the public-trace replay
bf3317a docs(readme): postmortems, the Phase 8 status and how to reproduce the numbers
```

plus this report.
