# ADR-0016: The resilience score is SLO headroom during the fault plus recovery after it

**Status:** Accepted, 2026-09-05.

## Context

Phase 5's exit criterion is that an experiment against a test service produces a resilience
score. A bare pass/fail meets the letter of it but leaves nothing to compare experiments or
services by, and "resilience" that measures only tolerance of a fault, not recovery once the
fault is gone, misses half of what the word means.

## Decision

The score is a number from 0 to 100 with two terms, weighted 0.7 and 0.3.

- **Headroom during the fault.** Each judged metric window, while the fault is injected, has
  a degradation `max(errorRate / errorRateMax, slowFraction / 0.01)` clipped to 1, where the
  slow fraction is the share of requests slower than `latencyP99MaxMs` and its bound is 1%.
  Headroom is `1 - degradation`; the term is the mean headroom over the fault windows. A
  service the fault never pushed toward its SLOs scores full headroom; one that sat at its
  limit scores zero.
- **Recovery after the fault.** Once the fault is removed the experiment observes up to
  `spec.analysis.recoveryWindows` further windows (default 4, 15 s each). Recovery is 1 if
  the first post-fault window is already within SLO, and steps down for each further window
  needed, to 0 if none of them is clean.

An aborted experiment scores 0, with the abort reason recorded. The status also carries the
worst error rate and slow fraction seen, so the number is explainable rather than opaque.

The abort decision is separate from the score and is not weighted or debounced: a single
window over either SLO aborts at once, because a false abort is the safe failure. The score
describes how an experiment that ran its course behaved; the abort is the safety response.

## Consequences

- Experiments and services are comparable on one axis, and the two terms make a low score
  legible (poor tolerance, slow recovery, or both).
- The recovery term extends an experiment's wall time past its `duration` by up to the
  recovery window, which the Phase 5 timings account for.
- The weights and the 1% slow-fraction bound are fixed in code; changing them changes every
  historical score's meaning, so a change is a new ADR, not a tweak.
