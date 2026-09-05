# Metrics: Baseline, Target, and Validation

Every subsystem is measured against a named baseline, not in isolation. **Baseline** is the status-quo alternative or the naive default this subsystem replaces. **Target** is what must be demonstrated before the phase counts as done. **Method** is exactly how it's measured, so the number is reproducible, not asserted.

## Why this matters

Without a platform layer, teams get infrastructure through a ticket to a platform engineer (hours to days per request) or raw Terraform written by the requesting developer. Platform engineering research consistently notes that one platform engineer can eliminate infrastructure bottlenecks for roughly fifty application developers, but only if the bottleneck-elimination is automated, not handled ticket-by-ticket. Compliance reconstruction today is a multi-day manual effort across scattered logs. Resilience gaps are typically discovered during real incidents, not before them. Every row below exists to remove one of these named costs.

## Baseline vs. target

| Subsystem | Baseline (status quo) | Target | Measurement method |
|---|---|---|---|
| Operator | Ticket-based manual provisioning: hours to days per request. | Standard-tier database reaches `Ready` in under 5 minutes end to end, zero data loss across concurrent scale/backup collisions. | The reconciler emits the histogram `kiln_tenantdatabase_time_to_ready_seconds` (CR creation to first `Ready`) on the manager's metrics endpoint, scraped by pod annotation. Run N=50 concurrent requests in CI, report p50/p95. Run the concurrency collision test suite to confirm zero data loss. |
| Provisioning/GitOps | Manual Terraform PR with human review: hours to a day before merge and apply. | Policy-compliant requests flow from submission to applied infrastructure with zero manual intervention. | Track event timestamps for `submitted`, `policy-checked`, `applied-in-ArgoCD`. Report the full latency distribution across a batch of synthetic requests. |
| Scheduler | Default kube-scheduler bin packing on a fixed synthetic workload trace, known cluster cost in $/hr. | Measurable % cost reduction on the identical workload trace, zero latency-class SLA violations. | Replay the identical workload trace through the default scheduler and the custom plugin on equivalent node pools. Compare actual instance-hours billed; log any placement that violates a declared latency-class constraint. |
| Progressive delivery | Manual on-call detection of a bad deploy (minutes to tens of minutes), manual rollback. | Automatic detection and rollback within a bounded window of regression onset, defined false-rollback ceiling on healthy-but-noisy canaries. | Injected synthetic regression suite covering real regressions and injected-noise scenarios. Record detection latency and false positive/negative rate across the full suite. |
| Chaos/resilience | Zero pre-production resilience coverage; failure modes discovered during real incidents. | Defined fault taxonomy (pod-kill, network partition, latency injection, resource exhaustion) covered by automated pre-production experiments, 100% of experiments respect the declared blast radius. | Run the full fault taxonomy against a test service on a schedule. Record any blast-radius violations; measure abort-trigger accuracy against forced SLO breaches. |
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
