// Parses docs/METRICS.md (the sole owner of every number on the site) into
// content/metrics.json. `--check` fails if the committed file would change, so a doc
// edit that moves a number cannot leave the site quietly disagreeing with it.
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { resolve } from "node:path";
import { provisioningClaim, claimMarker } from "../content/claim.ts";

const root = resolve(import.meta.dirname, "..");
const source = resolve(root, "../docs/METRICS.md");
const target = resolve(root, "content/metrics.json");
const readme = resolve(root, "../README.md");

const md = readFileSync(source, "utf8");

function must(re: RegExp, what: string, text: string = md): RegExpMatchArray {
  const m = text.match(re);
  if (!m) throw new Error(`docs/METRICS.md: could not find ${what} (${re})`);
  return m;
}

// A heading's body up to the next heading of the same or a higher level.
function section(heading: string): string {
  const start = md.indexOf(`\n${heading}\n`);
  if (start < 0) throw new Error(`docs/METRICS.md: no '${heading}' section`);
  const level = heading.match(/^#+/)![0].length;
  const rest = md.slice(start + 1 + heading.length);
  const next = rest.search(new RegExp(`\\n#{1,${level}} `));
  return next < 0 ? rest : rest.slice(0, next);
}

// The nth markdown table in a section: trimmed cells, the header row first, the
// separator row dropped.
function table(text: string, nth = 0): string[][] {
  const blocks: string[][][] = [];
  let current: string[][] | null = null;
  for (const line of text.split("\n")) {
    if (line.startsWith("|")) {
      if (!current) blocks.push((current = []));
      current.push(line.slice(1, -1).split("|").map((c) => c.trim()));
    } else current = null;
  }
  const t = blocks[nth];
  if (!t) throw new Error(`docs/METRICS.md: expected table ${nth + 1} in section`);
  return [t[0], ...t.slice(2)];
}

function columns(t: string[][], expected: string[], what: string): string[][] {
  const header = t[0].slice(0, expected.length);
  if (JSON.stringify(header) !== JSON.stringify(expected)) {
    throw new Error(`docs/METRICS.md: ${what} table header changed: ${t[0].join(" | ")}`);
  }
  return t.slice(1);
}

function int(s: string): number {
  return Number(s.replace(/,/g, ""));
}

const results = section("## Validation results");

const rows = columns(
  table(results),
  ["Request type", "Baseline (status quo)", "Platform p50", "Platform p95", "n", "Error rate", "Guardrail"],
  "validation",
).map((c) => ({
  requestType: c[0],
  baseline: c[1],
  p50: c[2],
  p95: c[3],
  n: Number(c[4]),
  errorRate: c[5],
  guardrail: c[6] === "-" ? null : c[6],
}));

const run = must(/CI run \[(\d+)\]\((https:[^)]+)\) on commit `([0-9a-f]+)`/, "the CI run reference", results);
const sched = must(/`kiln-scheduler` ([\d.]+)% cheaper than `default-scheduler` with zero latency-class violations \(the default placed (\d+) latency-sensitive pods on spot\)/, "the scheduler result");
const complete = must(/(\d+) audit rows for ([\dhms]+) of wall clock, (\d+) events acknowledged by Kafka, (\d+) publish failures, chain intact/, "the completeness line");
const identities = must(/\*\*Setup\.\*\* (\w+) developer identities/, "the identity count");
const traced = must(/([\d.]+) s from the audit rows, ([\d.]+) s from the claim's creation to the pod's Ready condition, ([\d.]+) s from the trace's root span/, "the traced request");
const guardrails = must(/rolled back on the mesh (\d+) s after its rollout started; the breach was aborted (\d+) s after the fault went live and the `iptables` rule was gone from the node (\d+) s after the `Aborted` row/, "the guardrail timings");

const outbox = must(
  /the under-crash proof \(Phase 8\) passed in CI run \[(\d+)\]\((https:[^)]+)\) on (\d{4}-\d{2}-\d{2}) on commit `([0-9a-f]+)`, after a harness bug had kept it red for (\d+) days/,
  "the Phase 8 proof",
  section("## Service level objectives"),
);

const comparison = section("## Detector comparison");
const detectors = columns(
  table(comparison),
  [
    "Detector",
    "Tuning",
    "Worst false rollback (healthy/noisy)",
    "step to 30%: detect p50 / p95, missed",
    "step to 2% (the H1 rate): detect p50 / p95, missed",
    "ramp 0.5% to 3% over 8 windows: detect p50 / p95, missed",
    "latency tail 5%: detect p50 / p95, missed",
  ],
  "detector comparison",
).map((c) => ({ detector: c[0], tuning: c[1], worstFalseRollback: c[2], step30: c[3], step2x: c[4], ramp: c[5], latencyTail: c[6] }));

const holdout = section("### Held-out evaluation on traffic the rules were not tuned on");
const classes = must(/Classes: (\d+), of which (\d+) from round 1 and (\d+) new\./, "the held-out class count", holdout);
const lowTraffic = must(/rolls back ([\d.]+)% of healthy canaries, all as `Inconclusive`/, "the low-traffic loss", holdout);
const retryStorm = must(/it rolls back ([\d.]+)% of healthy rollouts against the (\d+)% it promises/, "the retry-storm loss", holdout);
const heavyTail = must(/the sequential test misses ([\d.]+)% of regressions against the (\d+)% it promises; the burn-rate alert misses ([\d.]+)% and the threshold ([\d.]+)%/, "the heavy-tail loss", holdout);
const flagrant = must(/The flagrant step stays the burn-rate alert's win,\*\* (\d+) s against (\d+) s/, "the flagrant-step comparison", holdout);

const replay = section("## Public-trace replay");
const traceSetup = must(/([\d,]+) machines, ([\d,]+) tasks already running when the trace starts and ([\d,]+) new placements/, "the trace setup", replay);
const traceRows = columns(
  table(replay),
  ["Scenario", "Placement", "Billed $ (node-occupancy)", "vs default", "On-demand node-h", "Spot node-h", "Machines touched", "Latency-class on spot", "Unplaced", "Mean request util of occupied", "Placements/s"],
  "public-trace replay",
);
const fullCluster = traceRows
  .filter((c) => c[0] === "cluster carrying the trace's running tasks" && !c[1].startsWith("trace's own"))
  .map((c) => ({ placement: c[1], billed: c[2], vsDefault: c[3], machines: int(c[6]), latencyClassOnSpot: int(c[7]), unplaced: int(c[8]), util: c[9] }));
if (fullCluster.length !== 2) throw new Error("docs/METRICS.md: expected the default and kiln rows for the full cluster");

const binary = section("### The scheduler binary on the same trace");
const sideBySide = columns(
  table(binary, 1),
  ["Placement", "Billed $, model", "Billed $, binary", "vs default, model", "vs default, binary", "Latency-class on spot, model", "binary", "Machines touched, model", "binary", "Mean request util, model", "binary"],
  "model versus binary",
).map((c) => ({
  placement: c[0],
  billedModel: c[1],
  billedBinary: c[2],
  vsDefaultModel: c[3],
  vsDefaultBinary: c[4],
  latencyClassOnSpotModel: int(c[5]),
  latencyClassOnSpotBinary: int(c[6]),
  machinesModel: int(c[7]),
  machinesBinary: int(c[8]),
  utilModel: c[9],
  utilBinary: c[10],
}));

const scalability = section("## Controller scalability");
const scaleHeader = [
  "Rollouts",
  "Workers",
  "Client qps/burst",
  "Init wave",
  "Rollout wave",
  "Finished",
  "Reconciles",
  "Reconcile p50 / p99",
  "Tick lateness p50 / p99 / max (of 15 s)",
  "Ticks",
  "Queue depth max / mean",
  "Controller writes/s",
];
const scaleRows = (nth: number, what: string) =>
  columns(table(scalability, nth), scaleHeader, what).map((c) => ({
    rollouts: int(c[0]),
    workers: int(c[1]),
    clientBudget: c[2],
    rolloutWave: c[4],
    finished: int(c[5]),
    reconcileP50P99: c[7],
    tickLateness: c[8],
    queueDepthMaxMean: c[10],
    controllerWritesPerSecond: Number(c[11]),
  }));

const metrics = {
  source: {
    path: "docs/METRICS.md",
    section: "Validation results",
    runId: run[1],
    runUrl: run[2],
    commit: run[3],
  },
  setup: {
    identities: identities[1],
    tracedRequest: { auditRowsSeconds: Number(traced[1]), podReadySeconds: Number(traced[2]), traceSeconds: Number(traced[3]) },
  },
  validation: rows,
  scheduler: {
    cheaperPercent: Number(sched[1]),
    pluginLatencyClassViolations: 0,
    defaultSchedulerSpotPlacements: Number(sched[2]),
  },
  guardrails: {
    rollbackSeconds: Number(guardrails[1]),
    abortSeconds: Number(guardrails[2]),
    faultGoneAfterAbortSeconds: Number(guardrails[3]),
  },
  completeness: {
    auditRows: Number(complete[1]),
    wallClock: complete[2],
    kafkaAcknowledged: Number(complete[3]),
    publishFailures: Number(complete[4]),
    chainIntact: true,
  },
  outbox: {
    runId: outbox[1],
    runUrl: outbox[2],
    date: outbox[3],
    commit: outbox[4],
    redDays: Number(outbox[5]),
  },
  detectors: {
    comparison: detectors,
    holdout: {
      classes: Number(classes[1]),
      newClasses: Number(classes[3]),
      lowTrafficFalseRollback: `${lowTraffic[1]}%`,
      retryStormFalseRollback: `${retryStorm[1]}%`,
      alpha: `${retryStorm[2]}%`,
      heavyTailMiss: `${heavyTail[1]}%`,
      beta: `${heavyTail[2]}%`,
      heavyTailMissBurnRate: `${heavyTail[3]}%`,
      heavyTailMissThreshold: `${heavyTail[4]}%`,
      flagrantBurnRateSeconds: Number(flagrant[1]),
      flagrantSequentialSeconds: Number(flagrant[2]),
    },
  },
  trace: {
    machines: int(traceSetup[1]),
    runningTasks: int(traceSetup[2]),
    placements: int(traceSetup[3]),
    empty: sideBySide,
    full: fullCluster,
  },
  scale: {
    before: scaleRows(0, "controller scalability, before"),
    after: scaleRows(1, "controller scalability, after"),
  },
};

const next = JSON.stringify(metrics, null, 2) + "\n";

const provisioning = rows.find((r) => r.requestType === "Provisioning (standard Postgres)");
if (!provisioning) throw new Error("docs/METRICS.md: no provisioning row");
const claim = provisioningClaim(provisioning);
const readmeText = readFileSync(readme, "utf8");
const claimRe = new RegExp(`${claimMarker.open}\\r?\\n([^]*?)\\r?\\n${claimMarker.close}`);
const readmeClaim = readmeText.match(claimRe);
if (!readmeClaim) throw new Error(`README.md: no ${claimMarker.open} block`);
const readmeNext = readmeText.replace(claimRe, `${claimMarker.open}\n${claim}\n${claimMarker.close}`);

if (process.argv.includes("--check")) {
  const current = existsSync(target) ? readFileSync(target, "utf8") : "";
  if (current !== next) {
    console.error("content/metrics.json is out of date with docs/METRICS.md; run `pnpm pull:metrics` and commit the result");
    process.exit(1);
  }
  if (readmeClaim[1] !== claim) {
    console.error("README.md's provisioning claim is out of date with docs/METRICS.md; run `pnpm pull:metrics` and commit the result");
    process.exit(1);
  }
  console.log("content/metrics.json and the README claim match docs/METRICS.md");
} else {
  writeFileSync(target, next);
  writeFileSync(readme, readmeNext);
  console.log(`wrote content/metrics.json: ${rows.length} validation rows, run ${metrics.source.runId}, commit ${metrics.source.commit}; README claim: ${claim}`);
}
