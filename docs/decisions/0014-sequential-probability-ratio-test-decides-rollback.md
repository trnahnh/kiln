# ADR-0014: A capped sequential probability ratio test decides rollback, and confidence sizes the sub-steps

**Status:** Accepted, 2026-09-05

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#4-progressive-delivery-controller) rules out a raw error-rate threshold and asks for a minimum sample size plus a statistical significance check, with traffic steps that adapt to confidence. A per-window significance test looked at every analysis interval inflates the false-positive rate with every look. Two criteria must be judged, error rate and p99 latency, and the CRD gives both as absolute limits (`errorRateMax`, `latencyP99MaxMs`). Istio delivers counts and histogram buckets, not per-request samples.

## Decision

Each criterion is a Bernoulli sequential test in Wald's form. Errors: H0 p = `errorRateMax`, H1 p = `regressionFactor` x H0. Latency: p99 <= `latencyP99MaxMs` is equivalent to "at most 1% of requests slower than it", so H0 is a 1% tail with the same H1 multiplier; the tail count comes from the histogram, interpolated linearly inside the bucket containing the limit as `histogram_quantile` would. The configured alpha is split between the two criteria.

Per analysis window with n requests and x failures the evidence is x ln(p1/p0) + (n-x) ln((1-p1)/(1-p0)), capped at one third of the rollback bound A = ln((1-beta)/alpha) in either direction, so no single burst and no single quiet window decides a rollout. Two statistics accumulate the capped evidence:

- **Cumulative**, a CUSUM floored at zero for the whole rollout. Crossing A rolls back. The floor keeps a long healthy stretch from banking credit that would hide a slow-building regression.
- **Since checkpoint**, the plain sum since the last `stepPercentages` checkpoint was reached. Descending by |B| = -ln(beta/(1-alpha)) accepts that checkpoint. Confidence is the descent as a fraction of |B|, clamped to [0,1], taken as the minimum over the two criteria.

The minimum sample size, counted since the current checkpoint, gates every promote, shift or accept decision; the rollback check runs first on whatever evidence exists. Between checkpoints the canary weight is checkpoint + confidence x (distance to the next), so sub-steps grow as confidence grows; a window whose evidence moved toward H1 is an anomaly that holds traffic and halves the next sub-step. Reaching a checkpoint resets the checkpoint statistics, so every level is accepted on its own samples. The first checkpoint is entered outright, because without traffic there is no evidence. A checkpoint that neither accepts nor rolls back within `maxStepDuration` rolls back as `Inconclusive`, or `MetricsUnavailable` when no window could be read; a canary whose pods never become available rolls back as `CanaryUnavailable`.

Defaults: alpha 0.05, beta 0.10, regression factor 2, interval 15s, `maxStepDuration` 30m, all overridable in `spec.analysis`.

## Consequences

- Alpha is the false-rollback ceiling recorded in [`METRICS.md`](../METRICS.md); the simulation suite in `delivery-controller/internal/analysis` measures it on healthy, degrading and noisy-healthy traffic including bursty and spiking noise, and the numbers are in `TESTING.md`.
- The cap makes the bound approximate rather than exact Wald; the measured rate is the guarantee, not the formula. It also fixes detection latency for flagrant regressions at three windows.
- A regression that builds more slowly than the rollout completes can be promoted. That is a property of any finite canary; the design doc's wording that sub-steps *shrink* with confidence was inverted and is corrected in the same change as this ADR.
- The analysis package has no Kubernetes, Istio or Prometheus imports; the reconciler persists its state in `status.analysis` so a restarted controller resumes the same test.
