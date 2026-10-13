import type { ReactNode } from "react";
import metrics from "@/content/metrics.json";
import { DOCS, REPO } from "@/content/site";

const METRICS = `${DOCS}/METRICS.md`;
const POSTMORTEM = `${DOCS}/postmortems/2026-10-10-nightly-red-for-29-days.md`;

const n = (x: number) => x.toLocaleString("en-US");

// Sheet 7. The second pass over the two headline claims and the controller behind one of
// them: every figure is read from metrics.json, which pull-metrics.ts fills from METRICS.md.
export default function Evidence() {
  return (
    <div className="mt-12 space-y-16">
      <Trace />
      <Detectors />
      <Scale />
      <Outbox />
    </div>
  );
}

function Block({ title, lede, children }: { title: string; lede: ReactNode; children: ReactNode }) {
  return (
    <div>
      <h3 className="demo-title">{title}</h3>
      <p className="demo-lede">{lede}</p>
      {children}
    </div>
  );
}

function Trace() {
  const t = metrics.trace;
  const [defaultFull, kilnFull] = t.full;
  return (
    <Block
      title="Calc sheet: the scheduler on a public workload"
      lede={
        <>
          The first 93.5 minutes of the Google 2011 cluster trace: {n(t.machines)} machines, {n(t.placements)} placements,
          synthetic spot labels on half the machines. Once through the plugin&apos;s scoring function as a model, once
          through kube-scheduler itself on an envtest API server, with the default profile and the kiln profile as
          the config declares them. An empty cluster, so every idle machine can be released.
        </>
      }
    >
      <table className="validation mt-6">
        <thead>
          <tr>
            <th>Placement</th>
            <th>Billed $, model / binary</th>
            <th>vs default, model / binary</th>
            <th>Latency-class on spot, model / binary</th>
            <th>Machines touched, model / binary</th>
          </tr>
        </thead>
        <tbody>
          {t.empty.map((r) => (
            <tr key={r.placement}>
              <td data-label="Placement" className="text-fg">
                {r.placement}
              </td>
              <td data-label="Billed $" className="num">
                {r.billedModel} / {r.billedBinary}
              </td>
              <td data-label="vs default" className="num">
                {r.vsDefaultModel ? `${r.vsDefaultModel} / ${r.vsDefaultBinary}` : ""}
              </td>
              <td data-label="On spot" className="num">
                {r.latencyClassOnSpotModel} / {r.latencyClassOnSpotBinary}
              </td>
              <td data-label="Machines" className="num">
                {n(r.machinesModel)} / {n(r.machinesBinary)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-6 max-w-[88ch] text-[14px] leading-relaxed text-fg-muted">
        The binary agrees with the model on the bill to within a few percent and on the class guarantee exactly. On the
        cluster as the trace found it, already {defaultFull.util} requested and carrying {n(t.runningTasks)} tasks, the
        model bills ${kilnFull.billed} for both profiles: no machine ever empties, so placement alone saves nothing.
        What holds in both scenarios is the guarantee, {kilnFull.latencyClassOnSpot} production-priority tasks on
        reclaimable capacity against {defaultFull.latencyClassOnSpot} for an economics-blind placement. The binary did
        not finish the full cluster, so that row stands as the model&apos;s only;{" "}
        <a href={`${METRICS}#public-trace-replay`} className="link">
          the full tables say so
        </a>
        .
      </p>
    </Block>
  );
}

function Detectors() {
  const d = metrics.detectors;
  const h = d.holdout;
  return (
    <Block
      title="Test sheet: the canary decision against two other rules"
      lede={
        <>
          The sequential test at its defaults, a consecutive-breach threshold (the shape Flagger and Argo Rollouts
          ship) and a multi-window burn-rate alert (the shape the SRE Workbook gives), on the same seeded traffic, the
          alternatives tuned to the same false-rollback ceiling. Each cell is detection from onset, p50 / p95, and the
          share of regressions that promoted anyway.
        </>
      }
    >
      <table className="validation mt-6">
        <thead>
          <tr>
            <th>Detector</th>
            <th>Worst false rollback</th>
            <th>Step to 30%</th>
            <th>Step to 2x the limit</th>
            <th>Ramp over 8 windows</th>
            <th>Latency tail</th>
          </tr>
        </thead>
        <tbody>
          {d.comparison.map((r) => (
            <tr key={r.detector}>
              <td data-label="Detector" className="text-fg">
                {r.detector}
                <span className="mt-1 block text-[12px] text-fg-faint">{r.tuning}</span>
              </td>
              <td data-label="False rollback" className="num">
                {r.worstFalseRollback}
              </td>
              <td data-label="Step to 30%" className="num">
                {r.step30}
              </td>
              <td data-label="Step to 2x" className="num">
                {r.step2x}
              </td>
              <td data-label="Ramp" className="num">
                {r.ramp}
              </td>
              <td data-label="Latency tail" className="num">
                {r.latencyTail}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-6 max-w-[88ch] text-[14px] leading-relaxed text-fg-muted">
        Then {h.classes} traffic classes, {h.newClasses} of them new, split in two: each alternative tuned on one half
        and scored on the other, the sequential test untuned. Where it loses:
      </p>
      <ul className="mt-4 grid grid-cols-1 gap-x-10 gap-y-4 lg:grid-cols-2">
        <Loss head={`${h.lowTrafficFalseRollback} false rollbacks at 5 rps`}>
          every one as Inconclusive. The 500-request sample gate outlasts the step deadline at that rate, and the
          controller does not derive either field from the traffic it sees. A spec mismatch, named as one.
        </Loss>
        <Loss head={`${h.retryStormFalseRollback} false rollbacks on retry storms`}>
          against the {h.alpha} it promises. Correlated bursts break the independence the likelihood ratio assumes.
        </Loss>
        <Loss head={`${h.heavyTailMiss} of heavy-tailed regressions missed`}>
          against the {h.beta} it promises. The burn-rate alert misses {h.heavyTailMissBurnRate} and the threshold{" "}
          {h.heavyTailMissThreshold}: everyone loses on that traffic, the sequential test least.
        </Loss>
        <Loss head={`${h.flagrantBurnRateSeconds} s against ${h.flagrantSequentialSeconds} s on a flagrant step`}>
          the burn-rate alert&apos;s win, one window against the three the evidence cap needs.
        </Loss>
      </ul>
      <p className="mt-6 text-sm text-fg-faint">
        Every tuning point, with intervals, and the trade-off plot:{" "}
        <a href={`${METRICS}#held-out-evaluation-on-traffic-the-rules-were-not-tuned-on`} className="link">
          held-out evaluation
        </a>
        .
      </p>
    </Block>
  );
}

function Loss({ head, children }: { head: string; children: ReactNode }) {
  return (
    <li className="border-t border-hairline pt-4">
      <p className="text-[15px] leading-relaxed text-fg-muted">
        <span className="font-semibold text-fg">{head}</span>, {children}
      </p>
    </li>
  );
}

function Scale() {
  const s = metrics.scale;
  const rows = [...s.before.map((r) => ({ ...r, config: "as shipped" })), ...s.after.map((r) => ({ ...r, config: "fixed" }))];
  return (
    <Block
      title="Load sheet: a thousand rollouts at once"
      lede={
        <>
          N concurrent CanaryRollouts through the reconciler on an envtest API server. As shipped, the manager had
          controller-runtime&apos;s default client budget, 20 requests per second with a burst of 30, and one worker,
          because nothing had set either. The fix is three flags: the client budget and the reconcile concurrency.
        </>
      }
    >
      <table className="validation mt-6">
        <thead>
          <tr>
            <th>Rollouts</th>
            <th>Config</th>
            <th>Every rollout promoted in</th>
            <th>Promoted</th>
            <th>Reconcile p50 / p99</th>
            <th>Analysis tick late, p50 / p99 / max</th>
            <th>Controller writes/s</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={`${r.config}-${r.rollouts}`}>
              <td data-label="Rollouts" className="num">
                {n(r.rollouts)}
              </td>
              <td data-label="Config" className="text-fg">
                {r.config}
                <span className="mt-1 block text-[12px] text-fg-faint">
                  {r.workers} {r.workers === 1 ? "worker" : "workers"}, {r.clientBudget} qps/burst
                </span>
              </td>
              <td data-label="Wave" className="num">
                {r.rolloutWave}
              </td>
              <td data-label="Promoted" className="num">
                {n(r.finished)} of {n(r.rollouts)}
              </td>
              <td data-label="Reconcile" className="num">
                {r.reconcileP50P99}
              </td>
              <td data-label="Tick late" className="num">
                {r.tickLateness}
              </td>
              <td data-label="Writes/s" className="num">
                {r.controllerWritesPerSecond}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-6 max-w-[88ch] text-[14px] leading-relaxed text-fg-muted">
        Lateness in a canary controller is risk: a regression that should roll back in 30 s rolls back while the queue
        drains. The fix moves the wall rather than removing it; at a thousand rollouts the status patches alone reach
        the new budget. Next in line, measured: one live read of the VirtualService per reconcile, because unstructured
        reads bypass the cache, and Events at a quarter of the budget.{" "}
        <a href={`${METRICS}#controller-scalability`} className="link">
          Per-verb request rates
        </a>
        .
      </p>
    </Block>
  );
}

function Outbox() {
  const o = metrics.outbox;
  return (
    <Block
      title="Revision note: the proof that never ran"
      lede={
        <>
          The durable-outbox proof kills every controller, repeatedly and without grace, through a burst of requests,
          and requires each transition to land exactly one audit row with the chain intact.
        </>
      }
    >
      <p className="max-w-[88ch] text-[15px] leading-relaxed text-fg-muted">
        It failed on every nightly for {o.redDays} days after it was pushed, on a precondition the harness asserted
        instead of waiting for, and nothing alerted. An audit of every claimed number found the streak, the harness
        now waits for its own precondition, a red nightly opens an issue, and the proof passed in{" "}
        <a href={o.runUrl} className="link">
          run {o.runId}
        </a>{" "}
        on {o.date}. The{" "}
        <a href={POSTMORTEM} className="link">
          postmortem
        </a>{" "}
        is blameless and names its action items; the audit itself and the second pass are{" "}
        <a href={`${REPO}/blob/main/docs/TESTING.md`} className="link">
          methods in TESTING.md
        </a>
        .
      </p>
    </Block>
  );
}
