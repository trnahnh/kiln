import metrics from "./metrics.json";
import type { NodeId } from "@/lib/hero/params";
import { rowKeys } from "./site";
import { provisioningClaim } from "./claim";

export interface Readout {
  value: string;
  detail: string;
}

type Row = (typeof metrics.validation)[number];

function row(requestType: string): Row {
  const r = metrics.validation.find((x) => x.requestType === requestType);
  if (!r) throw new Error(`metrics.json has no row "${requestType}"`);
  return r;
}

export const source = metrics.source;
export const completeness = metrics.completeness;
export const validationRows = metrics.validation;

export const caption = provisioningClaim(row(rowKeys.provisioning));

export const readouts: Record<NodeId, Readout> = {
  gitops: {
    value: row(rowKeys.policy).p50,
    detail: "to deny a claim over the storage ceiling. Nothing reached the cluster.",
  },
  operator: {
    value: `${row(rowKeys.provisioning).p50} p50, ${row(rowKeys.provisioning).p95} p95`,
    detail: "for a standard Postgres to reach Ready, cross-checked against the pod's own Ready condition.",
  },
  scheduler: {
    value: `${metrics.scheduler.cheaperPercent}% cheaper`,
    detail: `than default-scheduler on the identical workload trace, with zero latency-class violations. The default put ${metrics.scheduler.defaultSchedulerSpotPlacements} latency-sensitive pods on spot.`,
  },
  delivery: {
    value: `${metrics.guardrails.rollbackSeconds} s`,
    detail: `from rollout start to rollback on the mesh for an injected regression. Healthy canaries promoted in ${row(rowKeys.healthyDeploy).p50} p50.`,
  },
  chaos: {
    value: `${metrics.guardrails.abortSeconds} s`,
    detail: `from fault to abort on an injected SLO breach, with the iptables rule gone from the node ${metrics.guardrails.faultGoneAfterAbortSeconds} s later.`,
  },
  audit: {
    value: `${row(rowKeys.auditQuery).p50} p50, ${row(rowKeys.auditQuery).p95} p95`,
    detail: `for an actor and time-range query. ${metrics.completeness.auditRows} rows, ${metrics.completeness.publishFailures} publish failures, chain intact.`,
  },
};

export const band: Record<NodeId, { value: string; label: string }> = {
  gitops: { value: row(rowKeys.policy).p50, label: "to deny an oversized claim at admission" },
  operator: {
    value: `${row(rowKeys.provisioning).p50} p50`,
    label: `standard Postgres to Ready, ${row(rowKeys.provisioning).p95} p95`,
  },
  scheduler: { value: `${metrics.scheduler.cheaperPercent}%`, label: "cheaper than default-scheduler, same trace" },
  delivery: { value: `${metrics.guardrails.rollbackSeconds} s`, label: "injected regression to rollback on the mesh" },
  chaos: {
    value: `${metrics.guardrails.abortSeconds} s`,
    label: `breach to abort, fault gone ${metrics.guardrails.faultGoneAfterAbortSeconds} s later`,
  },
  audit: {
    value: `${row(rowKeys.auditQuery).p50} p50`,
    label: `actor and time-range query, ${row(rowKeys.auditQuery).p95} p95`,
  },
};
