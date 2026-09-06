# ADR-0015: The chaos agent enforces blast radius and reverts on a lease dead-man switch

**Status:** Accepted, 2026-09-05.

## Context

Phase 5 injects real faults (`tc netem`, `iptables` DROP, cgroup CPU/memory pressure, pod
deletion) into running workloads. The platform invariant is that a chaos experiment's blast
radius is enforced by the fault-injection mechanism itself, structurally, and that
`abortOnSLOBreach` can never be disabled. A fault that outlives the experiment, or reaches a
pod outside the declared scope, is a production incident, so the design is a safety system
before it is a testing tool.

Two forces shape it. First, the mechanism that applies a network or cgroup fault must run on
the node, privileged, in the target pod's namespaces; the controller that decides the
timeline should not. Second, the thing that guarantees a fault is removed cannot be the same
controller that decided to inject it, or a controller crash leaves faults live.

## Decision

A privileged `kiln-chaos-agent` DaemonSet is the only component that applies or reverts a
network or cgroup fault, and it is the enforcement point for containment. Pod-kill is the
controller's, done through the API.

- **The agent watches the `ChaosExperiment` CR; there is no bespoke agent API.** The
  controller selects the target pods, floored to `maxReplicaPercentage` of the matching
  pods, and writes them to `status.targets` with a lease it renews every analysis interval.
  Each agent acts only on targets scheduled on its own node.
- **The agent re-derives the blast radius from its own cluster read before touching a pod.**
  It recomputes the matching pods and the floored cap, and faults at most that many,
  chosen deterministically, dropping any pod that no longer matches. A selection that
  exceeds the cap is contained here, not trusted. `maxReplicaPercentage` of a small replica
  count that floors to zero pods is rejected as `InvalidSpec`; it never rounds up.
- **A fault is reverted on any of four independent triggers:** the phase leaving `Running`,
  the CR being deleted, the lease lapsing, or the fault's own absolute deadline
  (`startedAt + duration`) passing on the agent's own clock. The lease is the dead-man
  switch: if the controller crashes, is partitioned, or is deleted, it stops renewing, and
  a per-node sweeper reverts every fault whose lease has lapsed within a second, with no
  controller involvement. An agent that restarts replays its on-disk ledger and reverts
  everything before serving.
- **A finalizer holds the CR until the lease has lapsed,** so a deleted experiment still
  provably cleans up before the object disappears.
- **An experiment targets only its own namespace,** enforced by the controller and, before
  the controller ever sees the object, by a fail-closed Kyverno `ValidatingPolicy` of the
  same shape as [ADR-0006](0006-org-rules-as-fail-closed-kyverno-validating-policies.md).
- **SLO windows are read source-reported** from Istio through Prometheus, because a latency
  or partition fault is invisible to the destination sidecar; the target and its callers
  must be meshed. No injection begins before a baseline snapshot is read, and an experiment
  that loses metrics for the timeout aborts rather than injecting blind.

## Consequences

- Containment does not depend on the controller being correct or even alive; the agent and
  the lease are the guarantees, and both are proven in the Phase 5 end-to-end test from the
  node itself (the `tc` qdisc and `iptables` rules inside the pod's namespace), never from
  the CR's status.
- The agent is privileged and shares the host PID namespace; that power is bounded by the
  CRs it reads and the scope it re-validates, not by what it could technically reach.
- On kind, a per-pod cgroup join for resource-exhaustion is environment-specific; the burner
  mechanics are proven by unit tests, and the end-to-end test asserts the experiment runs
  and scores. The network faults and pod-kill are proven end to end from the node.
