# Metrics: Baseline, Target, and Validation

Every subsystem is measured against a named baseline, not in isolation. **Baseline** is the status-quo alternative or the naive default this subsystem replaces. **Target** is what must be demonstrated before the phase counts as done. **Method** is exactly how it's measured, so the number is reproducible, not asserted.

## Why this matters

Without a platform layer, teams get infrastructure through a ticket to a platform engineer (hours to days per request) or raw Terraform written by the requesting developer. Platform engineering research consistently notes that one platform engineer can eliminate infrastructure bottlenecks for roughly fifty application developers, but only if the bottleneck-elimination is automated, not handled ticket-by-ticket. Compliance reconstruction today is a multi-day manual effort across scattered logs. Resilience gaps are typically discovered during real incidents, not before them. Every row below exists to remove one of these named costs.

## Baseline vs. target

| Subsystem | Baseline (status quo) | Target | Measurement method |
|---|---|---|---|
| Operator | Ticket-based manual provisioning: hours to days per request. | Standard-tier database reaches `Ready` in under 5 minutes end to end, zero data loss across concurrent scale/backup collisions. | The reconciler emits the histogram `kiln_tenantdatabase_time_to_ready_seconds` (CR creation to first `Ready`) on the manager's metrics endpoint, scraped by pod annotation. Run N=50 concurrent requests in CI, report p50/p95. Run the concurrency collision test suite to confirm zero data loss. |
| Provisioning/GitOps | Manual Terraform PR with human review: hours to a day before merge and apply. | Policy-compliant requests flow from submission to applied infrastructure with zero manual intervention. | Track event timestamps for `submitted`, `policy-checked`, `applied-in-ArgoCD`. Report the full latency distribution across a batch of synthetic requests. |
| Scheduler | Default kube-scheduler bin packing on a fixed synthetic workload trace, known cluster cost in $/hr. | Measurable % cost reduction on the identical workload trace, zero latency-class SLA violations. | Replay the identical workload trace through the default scheduler and the custom plugin on equivalent node pools. Compare actual instance-hours billed, where a node bills its hourly cost for every interval in which it hosts at least one trace pod (node-occupancy hours, read from the API server's pod bindings and container run times, standing in for an autoscaler removing empty nodes); log any placement that violates a declared latency-class constraint. |
| Progressive delivery | Manual on-call detection of a bad deploy (minutes to tens of minutes), manual rollback. | Automatic detection and rollback within a bounded window of regression onset; false-rollback ceiling of 5% (alpha, ADR-0014) on healthy-but-noisy canaries and a miss ceiling of 10% (beta) at twice the configured limit. | Injected synthetic regression suite covering real regressions and injected-noise scenarios: the seeded simulation classes in `delivery-controller/internal/analysis` (1000 rollouts each) report false-rollback and miss rates and decision latency per class, and the kind end-to-end run reports rollback latency from the first traffic split and promotion latency, both read from the VirtualService and the pods rather than the CR. |
| Chaos/resilience | Zero pre-production resilience coverage; failure modes discovered during real incidents. | Defined fault taxonomy (pod-kill, network partition, latency injection, resource exhaustion) covered by automated pre-production experiments, 100% of experiments respect the declared blast radius, and every SLO breach auto-aborts with the fault removed within a bounded window. | `TestPhase5Chaos` runs the full taxonomy against a test service. Blast-radius adherence is read from the node (the `iptables` chain appears only inside targeted pods' network namespaces). Abort-trigger accuracy is measured against a forced breach (2000 ms latency vs a 1000 ms p99 limit); the abort bound is that every injected fault is gone within 30 s of the first breached window being visible in Prometheus, verified from the node (the `tc netem` qdisc cleared), and with a 15 s scrape and a 5 s poll the observed clearance runs to roughly 45 s. |
| Audit/RBAC | Manual compliance log reconstruction across disparate systems, commonly a multi-day effort at audit time. | Actor/resource/time-range audit query returns complete results in under 1 second; zero undetected tampering; zero duplicate entries under Kafka redelivery. | Load-test the query endpoint. Run a tamper-injection suite that intentionally corrupts stored hashes and confirm detection. Run a Kafka redelivery simulation and confirm duplicates are caught by the idempotency constraint, not silently double-written. |

## Validation plan

Passing the per-subsystem targets above proves each piece works in isolation. It doesn't prove the platform solves the problem end to end. This experiment closes that gap.

1. **Synthetic team simulation.** Simulate a team of 10 developer identities submitting a realistic mix of requests over a simulated week: database provisioning, scaling events, deployments, and on-demand chaos tests, at a request rate consistent with the baseline research above.
2. **Instrument everything through the audit trail.** Every simulated action must produce a real event in the Audit/RBAC service, no exceptions. This is both a load test for that subsystem and the source of truth for the comparison below.
3. **Before/after comparison.** For each request type, report the platform's measured latency and error rate against the baseline figures above, not against an arbitrary internal target. The deliverable is a single comparison table: status quo time and failure mode, versus platform time and failure mode, per request type.
4. **Failure injection during the simulation.** At least one policy violation, one bad deploy, and one forced SLO breach must be injected during the simulated week, to prove the guardrails (policy denial, auto-rollback, auto-abort) actually fire under realistic conditions, not only in isolated unit tests.

The output of this plan is also the Phase 7 demo script (see [`ROADMAP.md`](ROADMAP.md)): a single end-to-end walkthrough plus the before/after comparison table. This is the artifact that answers "how do you know it actually works" and "how do you know it's better than what people do today."

## A note on baseline figures

Baselines cited from external platform-engineering research (not measured on a real org) are stated as approximations, not internal measurements. The relative improvement claimed by each target still holds even if the absolute baseline shifts, since the comparison is against the measurement method, not a single external number.

## Service level objectives

The targets above are exit criteria: thresholds a phase had to clear once. An SLO is a promise kept over a window, with a budget for breaking it. The objectives below are proposed from the validation evidence, not yet monitored over a real window; each names its indicator's source so that the first real window can be measured the same way the validation was. The policy is the usual one: a subsystem whose budget is spent in the window takes no feature change until the budget is back, and the exhaustion is a postmortem.

| Objective | Indicator (source) | Window and budget | Evidence so far |
|---|---|---|---|
| 95% of standard Postgres claims reach `Ready` within 60 s of the request being accepted | `PROVISION_REQUEST` to `PROVISION Ready` audit rows, equivalently the `kiln_tenantdatabase_time_to_ready_seconds` histogram | 28 days; 5% of claims | p95 16 s, n=10, plus the traced claim at 6.94 s (validation results below) |
| 100% of claims that violate an org rule are denied before anything is composed | `POLICY_DENY` rows versus objects on the cluster | an invariant, no budget: a single miss is an incident | 1/1 denied in the simulated week, every violation class denied in `TestPhase2` |
| A regression of at least twice the error-rate limit is rolled back within 3 minutes of its onset in 90% of rollouts | `DEPLOY Started` to `ROLLBACK` rows, paired by template hash, against the onset in the canary's own metrics | 28 days; 10% of real regressions | simulation p95 2m30s for the 2x step, 0 of 1000 missed; the one injected 30% regression on the cluster rolled back in 50 s |
| No more than 5% of healthy rollouts are rolled back | `ROLLBACK` rows whose canary never breached its limits over a window, judged after the fact from Prometheus | 28 days; 5% of healthy rollouts | simulation worst class 2.8%; 0 of 29 healthy deploys rolled back on the cluster |
| A chaos experiment that breaches its SLO has every fault removed from the node within 60 s of the breach | `CHAOS_EXPERIMENT Aborted` row and the `iptables`/`tc` state on the node | an invariant, no budget | abort 25 s after the fault went live, rule gone 1 s after the row |
| 99% of audit queries over an actor and time range return within 1 s with the same count `psql` gives | client round trip of `GET /v1/audit` and the psql cross-check | 28 days; 1% of queries | p95 108 ms, n=10, every count matching |
| Every audited transition has exactly one audit row | informer-observed transitions versus rows, and the chain verification | an invariant, no budget | proven for the simulated week (201 rows, 0 publish failures); the under-crash proof (Phase 8) has not yet passed in CI, see `docs/postmortems/2026-10-10-nightly-red-for-29-days.md` |

## Detector comparison

The canary decision ([ADR-0014](decisions/0014-sequential-probability-ratio-test-decides-rollback.md)) against the two rules a canary controller usually ships with, on the simulation suite's traffic models and seeds (method in [`TESTING.md`](TESTING.md#progressive-delivery-controller)): a per-window threshold that rolls back after N consecutive breaches, the shape of Flagger's and Argo Rollouts' metric checks, and a multi-window multi-burn-rate alert on the error budget, the shape of the SRE Workbook's alerting chapter. The alternatives are tuned in-sample on these classes, the sequential test is not; both alternatives drive a fixed schedule of four one-minute steps. Produced by `go test ./internal/analysis -run TestDetectorComparison` in `delivery-controller/` on 2026-10-10, verbatim:

| Detector | Tuning | Worst false rollback (healthy/noisy) | step to 30%: detect p50 / p95, missed | step to 2% (the H1 rate): detect p50 / p95, missed | ramp 0.5% to 3% over 8 windows: detect p50 / p95, missed | latency tail 5%: detect p50 / p95, missed |
|---|---|---|---|---|---|---|
| sequential test + CUSUM (this controller) | ADR-0014 defaults | 2.8% | 30s / 30s, 0.0% | 1m15s / 2m30s, 0.0% | 1m45s / 2m0s, 0.0% | 30s / 45s, 0.0% |
| threshold, N consecutive breaches | limit x1.5, 4 consecutive | 2.0% | 45s / 45s, 0.0% | 1m15s / 2m45s, 6.6% | 1m45s / 2m30s, 0.0% | 45s / 45s, 0.0% |
| multi-window multi-burn-rate | burn x1.25 (18.0/7.5/1.2) | 1.8% | 15s / 15s, 0.0% | 3m0s / 3m0s, 2.0% | 3m0s / 3m0s, 0.0% | 3m0s / 3m0s, 0.0% |

Per-class false rollback for each row (for a regression class, rollbacks before its onset):

| Detector | Tuning | healthy clean | healthy half of the limit | noisy steady at 90% of the limit | noisy bursty, long-run 80% of the limit | noisy one window at 10x the limit | step to 30% | step to 2% (the H1 rate) | ramp 0.5% to 3% over 8 windows | latency tail 5% |
|---|---|---|---|---|---|---|---|---|---|---|
| sequential test + CUSUM (this controller) | ADR-0014 defaults | 0.0% | 0.0% | 0.4% | 2.8% | 0.0% | 0.0% | 0.0% | 0.0% | 0.0% |
| threshold, N consecutive breaches | limit x1.5, 4 consecutive | 0.0% | 0.0% | 2.0% | 0.8% | 0.0% | 0.0% | 0.0% | 0.0% | 0.0% |
| multi-window multi-burn-rate | burn x1.25 (18.0/7.5/1.2) | 0.0% | 0.0% | 0.0% | 1.8% | 0.0% | 0.0% | 0.0% | 0.0% | 0.0% |

**Reading the table.**

- **At equal false-rollback cost, the sequential test is the only rule that catches every regression class within the rollout.** Its worst false-rollback rate, 2.8% on bursty traffic, is the ceiling the others are tuned to. The consecutive-breach threshold tuned to 2.0% matches it on the subtle 2x step at the median (1m15s) but misses 6.6% of those rollouts and is 15 s slower on the flagrant ones; the burn-rate alert tuned to 1.8% is the fastest on the flagrant step (15 s, one window) and the slowest on everything subtle (3 minutes, its longest window), because a 2x regression is a 2x burn and only its 1x window can see it.
- **The sweep shows why.** For the threshold rule, each step of N trades false rollbacks for misses at the limit: N=3 gives 8.0% false rollbacks, N=4 2.0% with 6.6% missed, N=5 0.1% with 18.9% missed. For the burn-rate rule the scale factor does the same: 1.0x gives 16.2% false rollbacks, 1.25x 1.8% with 2.0% missed, 1.5x 0.3% with 9.0% missed, 2x misses 80%. Neither rule has an operating point that is both under the ceiling and complete, because a fixed-window rule cannot accumulate evidence across windows; the CUSUM can. (`KILN_COMPARISON_SWEEP=1` logs every point.)
- **What this does not show.** All three rules see the same synthetic traffic: independent requests at a fixed rate with Bernoulli failures, bursty only in the one class that draws each window's rate from a Beta distribution. Real traffic has diurnal rate, correlated failures and retries; the burn-rate alert's long windows are a strength against those that this model cannot credit. The comparison is of the decision rules, not of the products that ship them.

## Public-trace replay

The placement model on a public workload: the first 93.5 minutes of the Google 2011 cluster trace, 12,477 machines, 132,982 tasks already running when the trace starts and 57,012 new placements (521 production-priority, mapped to latency-sensitive; 42,626 standard; 13,865 free-tier, mapped to batch), replayed through the plugin's scoring function, through a least-allocated-plus-balanced model of the default profile, and through the machine the trace itself recorded for each task (method and every assumption in [`TESTING.md`](TESTING.md#scheduler-plugin)). Prices are synthetic: a seeded half of the machines is spot at the kind cluster's ratio; the trace has none. Produced by `make -C scheduler-plugin trace` on 2026-10-10, verbatim (placements per second is this laptop's wall clock, a 16-thread Ryzen 7 7735HS):

| Scenario | Placement | Billed $ (node-occupancy) | vs default | On-demand node-h | Spot node-h | Machines touched | Latency-class on spot | Unplaced | Mean request util of occupied | Placements/s |
|---|---|---|---|---|---|---|---|---|---|---|
| empty cluster | default scheduler (least-allocated + balanced) | 723.06 | +0.0% | 5153.2 | 5215.5 | 12403 | 249 | 0 | 5.9% | 74410 |
| empty cluster | kiln-scheduler (cost-aware scoring) | 59.56 | -91.8% | 48.0 | 1870.1 | 2104 | 0 | 0 | 43.5% | 11466 |
| empty cluster | trace's own placement (Borg) | 541.87 | -25.1% | 3904.1 | 4026.7 | 10340 | 253 | 63 | 9.5% | n/a |
| cluster carrying the trace's running tasks | default scheduler (least-allocated + balanced) | 1329.75 | +0.0% | 9665.1 | 9781.8 | 2499 | 263 | 24305 | 92.3% | 326 |
| cluster carrying the trace's running tasks | kiln-scheduler (cost-aware scoring) | 1329.75 | -0.0% | 9665.1 | 9781.6 | 2638 | 0 | 24362 | 93.2% | 280 |
| cluster carrying the trace's running tasks | trace's own placement (Borg) | 1329.60 | -0.0% | 9665.1 | 9778.1 | 10340 | 253 | 63 | 115.1% | n/a |

Sensitivity to the share of machines labelled spot (`KILN_TRACE_SPOT_FRACTION`):

| Spot fraction | Scenario | Placement | Billed $ | vs default | Latency-class on spot |
|---|---|---|---|---|---|
| 0.25 | empty cluster | default scheduler (least-allocated + balanced) | 913.12 | +0.0% | 138 |
| 0.25 | empty cluster | kiln-scheduler (cost-aware scoring) | 53.83 | -94.1% | 0 |
| 0.25 | empty cluster | trace's own placement (Borg) | 688.23 | -24.6% | 121 |
| 0.25 | cluster carrying the trace's running tasks | default scheduler (least-allocated + balanced) | 1681.78 | +0.0% | 148 |
| 0.25 | cluster carrying the trace's running tasks | kiln-scheduler (cost-aware scoring) | 1681.77 | -0.0% | 0 |
| 0.25 | cluster carrying the trace's running tasks | trace's own placement (Borg) | 1681.63 | -0.0% | 121 |
| 0.5 | empty cluster | default scheduler (least-allocated + balanced) | 723.06 | +0.0% | 249 |
| 0.5 | empty cluster | kiln-scheduler (cost-aware scoring) | 59.56 | -91.8% | 0 |
| 0.5 | empty cluster | trace's own placement (Borg) | 541.87 | -25.1% | 253 |
| 0.5 | cluster carrying the trace's running tasks | default scheduler (least-allocated + balanced) | 1329.75 | +0.0% | 263 |
| 0.5 | cluster carrying the trace's running tasks | kiln-scheduler (cost-aware scoring) | 1329.75 | -0.0% | 0 |
| 0.5 | cluster carrying the trace's running tasks | trace's own placement (Borg) | 1329.60 | -0.0% | 253 |
| 0.75 | empty cluster | default scheduler (least-allocated + balanced) | 525.92 | +0.0% | 384 |
| 0.75 | empty cluster | kiln-scheduler (cost-aware scoring) | 62.05 | -88.2% | 0 |
| 0.75 | empty cluster | trace's own placement (Borg) | 396.42 | -24.6% | 379 |
| 0.75 | cluster carrying the trace's running tasks | default scheduler (least-allocated + balanced) | 969.22 | +0.0% | 362 |
| 0.75 | cluster carrying the trace's running tasks | kiln-scheduler (cost-aware scoring) | 969.21 | -0.0% | 0 |
| 0.75 | cluster carrying the trace's running tasks | trace's own placement (Borg) | 969.07 | -0.0% | 379 |

**Reading the table.**

- **On an empty cluster the node-occupancy rule hands the packing strategy an 88 to 94% saving.** That is the same artefact the kind replay's 45.5% carries: a machine bills only while it hosts a task, the default model spreads 57,012 tasks over 12,403 machines at 6% requested utilisation, the cost-aware score packs them onto about 2,100 at 43%. It is an upper bound for a fleet whose idle machines are released the moment they empty. The trace's own placement spreads too (10,340 machines), because Borg was placing onto a cluster that was already full.
- **On the cluster as the trace found it, the saving is zero.** With the running tasks loaded on their recorded machines the cluster is 92% requested; no machine ever empties, so every strategy bills the same to within a dollar, and 42% of the arrivals find no room under request accounting, for every strategy alike (Borg schedules on usage and overcommits, which this model does not). Placement alone saves nothing when nothing can be released; the money is in how much capacity consolidation can hand back, which is the fleet manager's job, not the scheduler's.
- **What holds in every scenario is the class guarantee.** Zero production-priority tasks on reclaimable capacity for the cost-aware placement, against 138 to 384 for the default model and 121 to 379 for the trace's own placement under the same labels, at every spot fraction. The latter two are not a critique of Borg or of the default scheduler, which know nothing of these labels; they show what any economics-blind placement does once reclaimable capacity exists.
- **Throughput.** On the empty cluster the cost-aware scoring places about 10,000 to 11,500 tasks per second with the 5% candidate sample, the simpler default model 40,000 to 74,000; the pure score is 87 ns per node with no allocations (`BenchmarkScore`). When almost nothing fits, both fall to a few hundred per second because the candidate scan walks all 12,477 machines before giving up; a real scheduler would hit the same wall and queue the pod. None of this is a measurement of the scheduler binary, which the envtest benchmark in `TESTING.md` covers.

## Validation results

The plan above ran as `TestPhase7Validation` (`e2e/phase7_test.go`, `e2e/phase7_sim_test.go`) in CI run [34085396203](https://github.com/trnahnh/kiln/actions/runs/34085396203) on commit `78e0a2a`, on a fresh five-node kind cluster after `TestPhase2` through `TestPhase6` had passed on the same cluster in the same job. The table below is the run's `validation-results` artifact, verbatim.

**Setup.** Ten developer identities (`dev01@kiln.sim` to `dev10@kiln.sim`), one Istio-injected namespace each holding a database claim, a fortio service behind a `CanaryRollout` and a meshed client at 25 requests per second. A seeded schedule (seed 7) places every request in one simulated week of five eight-hour working days, compressed into 20 minutes of wall clock; per identity one provision, two scales (the claim re-submitted with more storage), three deploys (a pod-template change) and two on-demand pod-kill experiments, all through `POST /v1/requests` or on the identity's own Deployment. Three injections at fixed times: one identity claims 500 GB against the 100 GB ceiling, one ships a version answering 30% of requests with 500, one runs a full network partition against a 20% error bound. Requests on the same resource queue behind each other; different resources and identities overlap. The rate is a stated assumption, since the research cited above gives none: about eight infrastructure requests per developer per week.

**Where every number comes from.** Latencies are differences between audit rows read with `psql` inside the Postgres pod, never from a CR's status or the service's query endpoint: provisioning is `PROVISION_REQUEST` to `PROVISION Ready`; scaling is the claim's re-submission row to `SCALE Applied`; a deploy is `DEPLOY Started` to `DEPLOY Promoted` or `ROLLBACK`, paired by template hash; a chaos test is `CHAOS_EXPERIMENT Started` to `Completed` or `Aborted`; the policy denial is the denied POST's HTTP span in Jaeger, found through the `POLICY_DENY` record's `traceparent` header; the audit query is the client's round trip. Each latency is cross-checked against what the cluster showed: the database pod's Ready condition (within 15 s, because the operator polls its StatefulSet every 10 s), the PVC's spec, the VirtualService flipping back to primary, and the `iptables` rule clearing inside the targeted pods' network namespaces on the node (within the 60 s abort bound), the others within 5 s plus one poll. The run also required every request to have its rows, the chain recomputed from all rows to hold and `/v1/audit/verify` to agree, and `kiln_audit_publish_failures_total` scraped by Prometheus to be zero. Error rate counts requests that neither completed nor were stopped by the guardrail they were meant to trip; the injected rows report the guardrail instead.

| Request type | Baseline (status quo) | Platform p50 | Platform p95 | n | Error rate | Guardrail |
|---|---|---|---|---|---|---|
| Provisioning (standard Postgres) | Manual Terraform PR with human review: hours to a day before merge and apply. | 11s | 16s | 10 | 0.0% (0/10) | - |
| Scaling (storage growth) | Manual Terraform PR with human review: hours to a day before merge and apply. | 49 ms | 140 ms | 20 | 0.0% (0/20) | - |
| Healthy deploy (canary promoted) | Manual on-call detection of a bad deploy (minutes to tens of minutes), manual rollback. | 2m14s | 2m30s | 29 | 0.0% (0/29) | - |
| Bad deploy (injected regression) | Manual on-call detection of a bad deploy (minutes to tens of minutes), manual rollback. | 50s | 50s | 1 | 0.0% (0/1) | auto-rollback fired 1/1 |
| Chaos test (pod-kill, completed) | Zero pre-production resilience coverage; failure modes discovered during real incidents. | 46s | 52s | 19 | 0.0% (0/19) | - |
| Chaos test (injected SLO breach) | Zero pre-production resilience coverage; failure modes discovered during real incidents. | 25s | 25s | 1 | 0.0% (0/1) | auto-abort, fault removed on the node fired 1/1 |
| Policy violation (storage over the ceiling) | Manual Terraform PR with human review: hours to a day before merge and apply. | 32 ms | 32 ms | 1 | 0.0% (0/1) | policy denial fired 1/1 |
| Audit query (actor, time range) | Manual compliance log reconstruction across disparate systems, commonly a multi-day effort at audit time. | 10 ms | 108 ms | 10 | 0.0% (0/10) | - |

**Reading the table.**

- **Provisioning/GitOps row.** On the REST path there is no Git commit and no ArgoCD apply: `submitted` is the `PROVISION_REQUEST` row, `policy-checked` is the `admission` span inside the request, and `composed` is the `TenantDatabase`'s creation. The Git-to-ArgoCD path is proven by `TestPhase2` on the same run, not measured here.
- **Scaling.** kind's local-path provisioner does not resize volumes, so `Applied` is the operator's PVC patch, cross-checked against the PVC's spec.
- **Scheduler.** Not a request type. `TestPhase3` on the same run replayed the identical 60-pod trace through both schedulers and billed node-occupancy hours from the API server: `kiln-scheduler` 45.5% cheaper than `default-scheduler` with zero latency-class violations (the default placed 4 latency-sensitive pods on spot). Every database pod the week provisioned was placed by `kiln-scheduler` as latency-sensitive.
- **The traced request.** The first subtest's claim was measured three ways: 6.94 s from the audit rows, 7 s from the claim's creation to the pod's Ready condition, 6.97 s from the trace's root span to the end of the operator's `PROVISION` span.
- **Bounds.** The pod-kill experiments abort at a 95% error rate or a 5000 ms p99; the injected breach at 20% or 1000 ms. A pod-kill strands in-flight requests for the 2 to 3 s their connections take to fail, so a 2000 ms p99 bound measures pod termination rather than the service, and it aborted a Phase 5 run in CI on that margin.
- **Guardrails.** The bad deploy was rolled back on the mesh 50 s after its rollout started; the breach was aborted 25 s after the fault went live and the `iptables` rule was gone from the node 1 s after the `Aborted` row; the oversized claim left no object on the cluster.
- **Completeness and load.** 201 audit rows for 23m13s of wall clock, 176 events acknowledged by Kafka, 0 publish failures, chain intact.

**What the validation found.** The chaos controller read SLO metrics for the targeted pods' `app` label, which is the Istio workload only for a Deployment named after its label; under canary delivery the serving pods belong to `<name>-primary`, so every experiment against a canary-managed service aborted `MetricsUnavailable` rather than scoring. Fail-closed, as designed, but wrong for the integrated platform; fixed before this run ([`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md#5-chaos-and-resilience-scoring-module), section 5).

**Reproducing.** `go test -tags=e2e -run TestPhase7Validation ./e2e/` against the bootstrapped cluster; `KILN_SIM_SCALE` (identities, minimum 3), `KILN_SIM_WEEK` (wall clock for the week) and `KILN_SIM_SEED` vary the run, `KILN_SIM_REPORT` names the table file. CI uploads it as the `validation-results` artifact of every `platform-e2e` job.
