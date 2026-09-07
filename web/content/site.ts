import type { NodeId } from "@/lib/hero/params";

export const REPO = "https://github.com/trnahnh/kiln";
export const DOCS = `${REPO}/blob/main/docs`;
export const ADR = `${DOCS}/decisions`;

export const site = {
  title: "kiln",
  description:
    "Self-service Kubernetes infrastructure. A request goes in; a policy-gated, cost-placed, canary-delivered, chaos-tested, audited resource comes out.",
  headline: "A request goes in. A hardened resource comes out.",
  headlineLines: ["A request goes in.", "A hardened resource comes out."],
  subline:
    "Policy-gated, cost-placed, canary-delivered, chaos-tested, and every step on a hash-chained audit log.",
  nav: [
    { label: "Design", href: `${DOCS}/SYSTEM_DESIGN.md` },
    { label: "Metrics", href: `${DOCS}/METRICS.md` },
    { label: "GitHub", href: REPO },
  ],
  actions: {
    primary: { label: "Read the system design", href: `${DOCS}/SYSTEM_DESIGN.md` },
    secondary: { label: "See the validation run" },
  },
};

export interface FlowItem {
  id: NodeId;
  title: string;
  problem: string;
}

export const flow: FlowItem[] = [
  {
    id: "gitops",
    title: "GitOps",
    problem:
      "A request is checked against org policy before anything is composed. A denied request stops here, logged, with nothing on the cluster.",
  },
  {
    id: "operator",
    title: "Operator",
    problem:
      "A status-driven state machine provisions, scales, backs up and restores per-tenant databases. Conflicting operations are rejected, never interleaved.",
  },
  {
    id: "scheduler",
    title: "Scheduler",
    problem:
      "Pods are placed by cost, fragmentation and preemption risk. Latency-sensitive pods are filtered off spot nodes before scoring runs.",
  },
  {
    id: "delivery",
    title: "Delivery",
    problem:
      "Canary traffic shifts as a sequential probability ratio test accumulates confidence. A regression rolls back on the mesh, not on a status field.",
  },
  {
    id: "chaos",
    title: "Chaos",
    problem:
      "A node agent injects the fault inside the target's own namespaces, caps the blast radius itself, and reverts on a lease the moment the controller stops renewing it.",
  },
  {
    id: "audit",
    title: "Audit",
    problem:
      "Every subsystem publishes to Kafka. One service holds the write credential, computes the hash chain, and rejects redelivered events by ID.",
  },
];

export const invariants = [
  {
    adr: "0006",
    text: "Org rules are fail-closed Kyverno policies evaluated at admission, before anything is provisioned.",
  },
  {
    adr: "0004",
    text: "Every resource the operator creates carries an owner reference to its custom resource, so nothing outlives it.",
  },
  {
    adr: "0015",
    text: "The chaos agent re-derives the blast radius from its own cluster read, and a fault reverts on a lease dead-man switch if the controller disappears.",
  },
  {
    adr: "0016",
    text: "A resilience score is SLO headroom plus recovery, reported only for a fault confirmed to have landed. A run that tested nothing never reads as a pass.",
  },
  {
    adr: "0017",
    text: "Audit publishing never blocks a reconcile. A Kafka outage stalls no provisioning, rollout or revert, and a dropped publish is counted, not lost.",
  },
  {
    adr: "0018",
    text: "The audit service alone computes the hash chain, under a single write credential no other subsystem holds.",
  },
  {
    adr: "0019",
    text: "The operator holds create-only access to secrets.",
  },
];

export function adrUrl(number: string, slug: string): string {
  return `${ADR}/${number}-${slug}.md`;
}

export const adrSlugs: Record<string, string> = {
  "0004": "backups-as-operator-fired-jobs-on-owned-pvc",
  "0006": "org-rules-as-fail-closed-kyverno-validating-policies",
  "0015": "chaos-agent-enforces-blast-radius-with-a-lease-dead-man-switch",
  "0016": "resilience-score-is-slo-headroom-plus-recovery",
  "0017": "audit-publishing-never-blocks-a-reconcile",
  "0018": "the-audit-service-alone-computes-the-hash-chain",
  "0019": "the-operator-holds-create-only-secret-access",
};

export const rowKeys = {
  provisioning: "Provisioning (standard Postgres)",
  scaling: "Scaling (storage growth)",
  healthyDeploy: "Healthy deploy (canary promoted)",
  badDeploy: "Bad deploy (injected regression)",
  chaosCompleted: "Chaos test (pod-kill, completed)",
  chaosBreach: "Chaos test (injected SLO breach)",
  policy: "Policy violation (storage over the ceiling)",
  auditQuery: "Audit query (actor, time range)",
} as const;
