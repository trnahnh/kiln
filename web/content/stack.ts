import type { NodeId } from "@/lib/hero/params";

export interface Artifact {
  title: string;
  lang: "yaml" | "json" | "text";
  code: string;
}

export interface Specimen {
  id: NodeId;
  title: string;
  role: string;
  stack: string[];
  problems: string[];
  adr: string;
  artifact: Artifact;
}

export const specimens: Specimen[] = [
  {
    id: "gitops",
    title: "GitOps and policy",
    role: "Turns a claim into real resources, after the org's rules have said yes.",
    stack: ["Crossplane v2", "ArgoCD", "Kyverno", "CEL", "Terraform"],
    problems: [
      "A developer asks for a standard Postgres and never names an instance class, backup retention or network placement. Those are platform defaults, with a custom tier as the escape hatch.",
      "Rules are fail-closed validating policies at admission. A denied claim leaves nothing on the cluster and a POLICY_DENY row in the audit log.",
      "ArgoCD heals drift on anything stateless and only flags it on anything stateful, so a live database is never reverted without a human.",
    ],
    adr: "0006",
    artifact: {
      title: "DatabaseClaim and the rule that denied one",
      lang: "yaml",
      code: `apiVersion: platform.internal/v1alpha1
kind: DatabaseClaim
metadata:
  name: checkout-db
  namespace: team-checkout
spec:
  parameters:
    tier: standard          # standard | custom
    storageGB: 500          # the validation week's injected violation
    tags:
      team: checkout
      costCenter: eng-platform

# gitops/policies/databaseclaim-org-rules.yaml
- expression: object.spec.parameters.storageGB <= 100
  message: "POLICY_DENIED rule=storage-ceiling: spec.parameters.storageGB must be at most 100"`,
    },
  },
  {
    id: "operator",
    title: "Operator",
    role: "Owns the lifecycle of every per-tenant database: provision, scale, back up, restore.",
    stack: ["Go", "kubebuilder", "controller-runtime", "envtest"],
    problems: [
      "Re-running a reconcile on the same state produces no side effects. Every step reads the cluster before acting.",
      "A scale request arriving mid-backup is rejected and requeued, never interleaved. The phase field is the single source of truth for what is in flight.",
      "Every Secret, Service, StatefulSet, volume and Job carries an owner reference to the custom resource, so deleting it garbage-collects everything, backups included.",
    ],
    adr: "0004",
    artifact: {
      title: "TenantDatabase, the object the operator reconciles",
      lang: "yaml",
      code: `apiVersion: platform.internal/v1
kind: TenantDatabase
metadata:
  name: team-checkout-db
spec:
  engine: postgres            # postgres | redis
  version: "16"
  storageGB: 20               # may grow, never shrink
  backupSchedule: "0 3 * * *"
  tier: standard
status:
  phase: Ready                # Provisioning | Ready | Backing Up | Restoring | Failed
  lastBackupTime: "2026-09-01T03:00:00Z"
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2026-09-01T02:58:11Z"`,
    },
  },
  {
    id: "scheduler",
    title: "Scheduler plugin",
    role: "Places pods by cost, fragmentation and preemption risk instead of default bin packing.",
    stack: ["Go", "scheduler framework", "Prometheus", "AWS spot pricing"],
    problems: [
      "Three terms pull against each other: cost, the gaps a placement leaves on other nodes, and the chance a spot node is reclaimed. The weights are explicit and must sum to 100.",
      "Latency-sensitive pods never land on spot. The workload-class filter removes those nodes before scoring runs, so no score can override it.",
      "Node economics are labels with pluggable price sources, so the same plugin runs on kind and on EKS.",
    ],
    adr: "0009",
    artifact: {
      title: "The placement contract and the scoring rule",
      lang: "yaml",
      code: `# on the pod
metadata:
  labels:
    kiln.platform.internal/workload-class: latency-sensitive   # never placed on spot
spec:
  schedulerName: kiln-scheduler

# on the node
kiln.platform.internal/capacity-type: spot        # spot | on-demand
kiln.platform.internal/hourly-cost: "0.037"       # USD per hour
kiln.platform.internal/preemption-risk: "0.05"    # 0 to 1, spot only

# CostAware score per feasible node, each term in [0, 1]
score = 50 × cost + 30 × fragmentation + 20 × preemption`,
    },
  },
  {
    id: "delivery",
    title: "Progressive delivery",
    role: "Canary rollout for every deployment, with a statistical decision to promote or roll back.",
    stack: ["Go", "Istio", "Prometheus", "SPRT", "CUSUM"],
    problems: [
      "A raw error-rate threshold false-triggers on noisy canaries and under-reacts to slow regressions. A sequential probability ratio test per criterion, with per-window evidence capped, decides instead.",
      "Checkpoints must each be accepted; between them the canary weight grows with the confidence earned so far.",
      "What proves a rollback is the VirtualService's weights, the canary pods being gone and real requests through the mesh returning healthy, never the CR's own status.",
    ],
    adr: "0014",
    artifact: {
      title: "CanaryRollout, with the decision's parameters",
      lang: "yaml",
      code: `apiVersion: platform.internal/v1
kind: CanaryRollout
metadata:
  name: checkout-service-rollout
  namespace: team-checkout
spec:
  targetDeployment: checkout-service
  successCriteria:
    errorRateMax: 0.01          # null hypothesis of the error-rate test
    latencyP99MaxMs: 300        # at most 1% of requests slower than this
    minSampleSize: 500          # requests before any decision
  stepPercentages: [5, 20, 50, 100]
  analysis:
    interval: 15s
    alpha: 0.05                 # false-rollback ceiling
    beta: 0.1                   # missed-regression ceiling
    regressionFactor: 2         # alternative hypothesis = 2x the limit
    drainGrace: 10s`,
    },
  },
  {
    id: "chaos",
    title: "Chaos and resilience",
    role: "Injects controlled failure against a service and scores how it held up against its SLOs.",
    stack: ["Go", "controller-runtime", "tc netem", "iptables", "cgroups"],
    problems: [
      "A privileged node agent is the only thing that applies or reverts a fault, inside the target pod's own namespaces. It re-derives the blast radius from its own cluster read.",
      "Every fault carries a lease the controller renews. If the controller dies, the lease lapses and a per-node sweeper reverts the fault with no controller involved.",
      "A score is reported only for a fault confirmed to have landed. An injection failure aborts the experiment and writes no score.",
    ],
    adr: "0015",
    artifact: {
      title: "ChaosExperiment, with the cap the agent enforces",
      lang: "yaml",
      code: `apiVersion: platform.internal/v1
kind: ChaosExperiment
metadata:
  name: checkout-partition-test
  namespace: team-checkout
spec:
  target:
    labelSelector: app=checkout-service
    maxReplicaPercentage: 30    # floored to whole pods; floors to zero -> rejected
  faultType: network-partition  # pod-kill | network-partition | latency-injection | resource-exhaustion
  duration: 5m
  abortOnSLOBreach:             # required; cannot be disabled per experiment
    errorRateMax: 0.05
    latencyP99MaxMs: 1000
status:
  phase: Aborted                # Scheduled | Running | Completed | Aborted
  abortReason: SLOBreach
  faultsCleared: true           # true only once every agent's lease has lapsed`,
    },
  },
  {
    id: "audit",
    title: "Audit and RBAC",
    role: "The tamper-evident record of everything, and the gate on who may ask for anything.",
    stack: ["Java 21", "Spring Boot", "Spring Security", "Kafka", "PostgreSQL"],
    problems: [
      "Every subsystem publishes to one Kafka topic. Only this service holds the write credential to the table, and only it computes the hash chain, under an advisory lock.",
      "Event IDs are UUID v5 of the resource, action and transition, so a retried reconcile or a redelivered record is stored once. The unique constraint is the only dedup.",
      "RBAC here decides who may submit. Policy at admission decides what a request may contain. Neither repeats the other's check.",
    ],
    adr: "0018",
    artifact: {
      title: "The wire event, and how a row's hash is computed",
      lang: "json",
      code: `{
  "eventId": "6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10",
  "actor": "dev03@kiln.sim",
  "action": "PROVISION",
  "resource": "TenantDatabase/dev03/dev03-db",
  "timestamp": "2026-09-07T14:02:11.418213Z",
  "details": {"outcome": "Ready"}
}

hash = sha256( prev_hash + "\\n" + event_id + "\\n" + actor + "\\n" + action
             + "\\n" + resource + "\\n" + occurred_at + "\\n" + canonical(details) )`,
    },
  },
];

export const adrSlugsExtra: Record<string, string> = {
  "0009": "workload-class-label-gates-spot-placement",
  "0014": "sequential-probability-ratio-test-decides-rollback",
};
