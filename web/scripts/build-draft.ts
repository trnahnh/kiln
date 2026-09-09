// Builds the drawing's committed assets: public/draft/model.json (the six blocks, their
// two layouts and the measured values stamped from content/metrics.json),
// public/draft/drawing.svg (the finished sheet, the fallback and social-card source) and
// docs/assets/architecture.svg (the same sheet annotated, which the root README shows).
// Run with `pnpm build:draft`; CI rebuilds them all and fails on any diff.
import { mkdirSync, readFileSync, writeFileSync, statSync } from "node:fs";
import { basename, resolve } from "node:path";
import type { DraftModel } from "../lib/draft/model.ts";
import { renderSvg, type SvgPalette } from "../lib/draft/svg.ts";

const root = resolve(import.meta.dirname, "..");
const metrics = JSON.parse(readFileSync(resolve(root, "content/metrics.json"), "utf8"));

const row = (name: string) => {
  const r = metrics.validation.find((x: { requestType: string }) => x.requestType === name);
  if (!r) throw new Error(`metrics.json has no row "${name}"`);
  return r;
};

const model: DraftModel = {
  version: 1,
  blocks: [
    { id: "gitops", label: "GITOPS", stack: "Crossplane / ArgoCD / Kyverno", value: row("Policy violation (storage over the ceiling)").p50, measure: "policy denial" },
    { id: "operator", label: "OPERATOR", stack: "Go / kubebuilder", value: `${row("Provisioning (standard Postgres)").p50} p50`, measure: "claim to Ready" },
    { id: "scheduler", label: "SCHEDULER", stack: "scheduler framework / Prometheus", value: `${metrics.scheduler.cheaperPercent}%`, measure: "cheaper, same trace" },
    { id: "delivery", label: "DELIVERY", stack: "Istio / SPRT", value: `${metrics.guardrails.rollbackSeconds} s`, measure: "regression to rollback" },
    { id: "chaos", label: "CHAOS", stack: "tc / iptables / lease", value: `${metrics.guardrails.abortSeconds} s`, measure: "breach to abort" },
    { id: "audit", label: "AUDIT", stack: "Spring / Kafka / Postgres", value: `${row("Audit query (actor, time range)").p50} p50`, measure: "actor, time-range query" },
  ],
  layouts: {
    landscape: {
      block: { w: 2.4, d: 1.6, h: 1.1 },
      positions: [[0, 0], [4.2, 0], [8.4, 0], [8.4, 4.6], [4.2, 4.6], [0, 4.6]],
    },
    // Along the plan diagonal x = z, which the isometric camera projects as a vertical
    // column, with a lateral zigzag so consecutive blocks do not overlap on screen.
    portrait: {
      block: { w: 2.0, d: 1.4, h: 1.0 },
      positions: [0, 1, 2, 3, 4, 5].map((i) => {
        const base = i * 2.7;
        const s = (i % 2 === 0 ? -1 : 1) * 1.15;
        return [base + s, base - s] as [number, number];
      }),
    },
  },
  title: "GENERAL ARRANGEMENT",
  subtitle: "REQUEST FLOW",
  rev: metrics.source.commit,
  checked: `CI RUN ${metrics.source.runId}`,
  checkedUrl: metrics.source.runUrl,
};

function paletteFromCss(): SvgPalette {
  const css = readFileSync(resolve(root, "app/globals.css"), "utf8");
  const read = (name: string): string => {
    const m = css.match(new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6}|rgba?\\([^)]+\\))`));
    if (!m) throw new Error(`app/globals.css does not define --${name}`);
    return m[1];
  };
  return { ground: read("ground"), line: read("line"), faint: read("line-faint"), accent: read("accent") };
}

const outDir = resolve(root, "public/draft");
mkdirSync(outDir, { recursive: true });
const jsonPath = resolve(outDir, "model.json");
writeFileSync(jsonPath, JSON.stringify(model) + "\n");
const svgPath = resolve(outDir, "drawing.svg");
writeFileSync(svgPath, renderSvg(model, paletteFromCss()));
const cardPath = resolve(outDir, "card.svg");
writeFileSync(cardPath, renderSvg(model, paletteFromCss(), { width: 1200, height: 630 }));

const docsDir = resolve(root, "../docs/assets");
mkdirSync(docsDir, { recursive: true });
const readmePath = resolve(docsDir, "architecture.svg");
writeFileSync(readmePath, renderSvg(model, paletteFromCss(), { sheet: "1 OF 1", annotate: true }));

const sizes = [jsonPath, svgPath, cardPath, readmePath].map((f) => `${basename(f)} ${statSync(f).size} B`);
console.log(sizes.join(", "));
