# ADR-0009: A workload-class pod label gates spot placement, and the gate runs before scoring

**Status:** Accepted, 2026-09-06

## Context

[`SYSTEM_DESIGN.md`](../SYSTEM_DESIGN.md#3-costgpu-aware-scheduler-plugin) requires that latency-sensitive pods never land on cheap-but-reclaimable nodes regardless of score, and CLAUDE.md's invariants forbid enforcing that through anything a weighting could outvote. Pods need a way to declare their class, and the scheduler needs a rule for pods that declare nothing. Alternatives were deriving the class from a PriorityClass threshold, or treating unlabelled pods as eligible for spot.

## Decision

Pods declare their class with the label `kiln.platform.internal/workload-class`, one of `latency-sensitive`, `standard`, `batch`. A pod without the label, or with an unrecognised value, is treated as `latency-sensitive`.

The CostAware plugin implements the rule in the scheduler framework's Filter extension point: for a latency-sensitive pod every node whose capacity type is spot is returned `UnschedulableAndUnresolvable`. Score and PreScore never see those nodes. A node whose economics cannot be resolved counts as on-demand, so an unlabelled node is a safe but expensive target, never a cheap one.

## Consequences

- No combination of weights can place a latency-sensitive pod on spot; the class filter is structural, and the unit tests pin it with an all-spot cluster where such a pod is simply unschedulable.
- Teams opt into savings by labelling `standard` or `batch`; the safe default means a workload nobody has looked at is never reclaimed underneath its users.
- The label is the only class signal. PriorityClass keeps meaning urgency, not placement policy.
- The replay test counts a latency-sensitive pod on a spot node as an SLA violation; the exit criterion requires zero under kiln-scheduler.
