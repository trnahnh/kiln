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

function must(re: RegExp, what: string): RegExpMatchArray {
  const m = md.match(re);
  if (!m) throw new Error(`docs/METRICS.md: could not find ${what} (${re})`);
  return m;
}

const results = md.slice(md.indexOf("## Validation results"));
if (results.length === md.length) throw new Error("docs/METRICS.md: no '## Validation results' section");

const tableLines = results
  .split("\n")
  .filter((l) => l.startsWith("|"))
  .map((l) => l.slice(1, -1).split("|").map((c) => c.trim()));
const header = tableLines[0];
const expected = ["Request type", "Baseline (status quo)", "Platform p50", "Platform p95", "n", "Error rate", "Guardrail"];
if (JSON.stringify(header) !== JSON.stringify(expected)) {
  throw new Error(`docs/METRICS.md: validation table header changed: ${header.join(" | ")}`);
}
const rows = tableLines.slice(2).map((c) => ({
  requestType: c[0],
  baseline: c[1],
  p50: c[2],
  p95: c[3],
  n: Number(c[4]),
  errorRate: c[5],
  guardrail: c[6] === "-" ? null : c[6],
}));

const run = must(/CI run \[(\d+)\]\((https:[^)]+)\) on commit `([0-9a-f]+)`/, "the CI run reference");
const sched = must(/`kiln-scheduler` ([\d.]+)% cheaper than `default-scheduler` with zero latency-class violations \(the default placed (\d+) latency-sensitive pods on spot\)/, "the scheduler result");
const complete = must(/(\d+) audit rows for ([\dhms]+) of wall clock, (\d+) events acknowledged by Kafka, (\d+) publish failures, chain intact/, "the completeness line");
const identities = must(/\*\*Setup\.\*\* (\w+) developer identities/, "the identity count");
const traced = must(/([\d.]+) s from the audit rows, ([\d.]+) s from the claim's creation to the pod's Ready condition, ([\d.]+) s from the trace's root span/, "the traced request");
const guardrails = must(/rolled back on the mesh (\d+) s after its rollout started; the breach was aborted (\d+) s after the fault went live and the `iptables` rule was gone from the node (\d+) s after the `Aborted` row/, "the guardrail timings");

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
